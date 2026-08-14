#!/bin/sh
set -eu

VERSION=${VERSION:?set VERSION to the release version without a v prefix}
COMMIT=${COMMIT:?set COMMIT to the exact release commit}
REQUIRE_TAG=${REQUIRE_TAG:-0}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_DIR="$REPO_DIR/dist"

case "$VERSION" in
	v* | *[!0-9A-Za-z.-]* | "") echo "release: VERSION must omit v and contain only version characters" >&2; exit 2 ;;
esac
case "$REQUIRE_TAG" in
	0 | 1) ;;
	*) echo "release: REQUIRE_TAG must be 0 or 1" >&2; exit 2 ;;
esac

cd "$REPO_DIR"
HEAD_COMMIT=$(git rev-parse HEAD)
RESOLVED_COMMIT=$(git rev-parse "$COMMIT^{commit}")
if [ "$HEAD_COMMIT" != "$RESOLVED_COMMIT" ]; then
	echo "release: COMMIT must identify the checked-out HEAD" >&2
	exit 2
fi
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
	echo "release: working tree must be clean" >&2
	exit 2
fi
if [ "$REQUIRE_TAG" = 1 ]; then
	TAG_COMMIT=$(git rev-list -n 1 "v$VERSION" 2>/dev/null || true)
	if [ "$TAG_COMMIT" != "$RESOLVED_COMMIT" ]; then
		echo "release: tag v$VERSION must point at COMMIT" >&2
		exit 2
	fi
fi

SOURCE_COMMIT_DATE=$(git show -s --format=%cs "$RESOLVED_COMMIT")
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/omnihub-release.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT HUP INT TERM

# 二进制实际引用的 module build list 是第三方许可清单的唯一来源。
# 任一模块没有顶层许可文件就停止发布，避免静默遗漏归属或 NOTICE。
MODULES_FILE="$WORK_DIR/modules.txt"
LICENSES_DIR="$WORK_DIR/THIRD_PARTY_LICENSES"
NOTICES_FILE="$WORK_DIR/THIRD_PARTY_NOTICES.txt"
mkdir -p "$LICENSES_DIR"
go list -deps -f '{{with .Module}}{{if ne .Path "github.com/ylxmf2005/omnihub"}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}' ./cmd/omnihub | sort -u > "$MODULES_FILE"
{
	echo "OmniHub third-party notices"
	echo
	echo "Generated from the dependency build list for commit $RESOLVED_COMMIT with $(go version)."
	echo "The corresponding license and NOTICE files are included under THIRD_PARTY_LICENSES/."
	echo
} > "$NOTICES_FILE"

while IFS='|' read -r module version directory; do
	[ -n "$module" ] || continue
	safe_name=$(printf '%s@%s' "$module" "$version" | tr '/@' '__' | tr -cd 'A-Za-z0-9._-')
	target="$LICENSES_DIR/$safe_name"
	mkdir -p "$target"
	found=0
	files=
	for candidate in "$directory"/LICENSE* "$directory"/COPYING* "$directory"/NOTICE*; do
		[ -f "$candidate" ] || continue
		cp "$candidate" "$target/$(basename "$candidate")"
		files="$files $(basename "$candidate")"
		found=1
	done
	if [ "$found" != 1 ]; then
		echo "release: no top-level license file found for $module $version" >&2
		exit 1
	fi
	printf '%s %s:%s\n' "$module" "$version" "$files" >> "$NOTICES_FILE"
done < "$MODULES_FILE"

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

build_archive() {
	goos=$1
	goarch=$2
	format=$3
	package="omnihub_${VERSION}_${goos}_${goarch}"
	root="$WORK_DIR/$package"
	binary="$root/omnihub"
	if [ "$goos" = windows ]; then
		binary="$binary.exe"
	fi

	mkdir -p "$root"
	env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -buildvcs=true \
		-ldflags "-s -w -X main.releaseVersion=$VERSION -X main.releaseCommit=$RESOLVED_COMMIT -X main.releaseDate=$SOURCE_COMMIT_DATE" \
		-o "$binary" ./cmd/omnihub
	cp LICENSE README.md "$NOTICES_FILE" "$root/"
	cp -R "$LICENSES_DIR" "$root/"
	{
		echo "Version: $VERSION"
		echo "Commit: $RESOLVED_COMMIT"
		echo "Source commit date: $SOURCE_COMMIT_DATE"
		echo "Builder: $(go version)"
	} > "$root/BUILD_INFO.txt"

	# 每个压缩包只有一个同名顶层目录，解压不会污染当前目录。
	case "$format" in
		tar)
			COPYFILE_DISABLE=1 tar -C "$WORK_DIR" -czf "$DIST_DIR/$package.tar.gz" "$package"
			;;
		zip)
			(cd "$WORK_DIR" && zip -qr "$DIST_DIR/$package.zip" "$package")
			;;
	esac
}

build_archive darwin arm64 tar
build_archive linux amd64 tar
build_archive windows amd64 zip

(
	cd "$DIST_DIR"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 omnihub_* > checksums.txt
		shasum -a 256 -c checksums.txt
	else
		sha256sum omnihub_* > checksums.txt
		sha256sum -c checksums.txt
	fi
)

echo "release: wrote $DIST_DIR for version $VERSION at $RESOLVED_COMMIT"
