// Package omnihubskill 暴露随 OmniHub 二进制分发的 Agent 使用说明。
package omnihubskill

import _ "embed"

// Markdown 是 OmniHub Skill 的唯一文档来源。
//
//go:embed SKILL.md
var Markdown string
