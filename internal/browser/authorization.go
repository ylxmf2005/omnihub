package browser

import (
	"github.com/ylxmf2005/omnihub/internal/registry"
)

// AuthorizationForChannel 从已验证 Catalog 投影唯一可信浏览器授权，供 Query 与 Dashboard 共用。
func AuthorizationForChannel(catalog *registry.Catalog, channelID string) (AuthorizationDescriptor, error) {
	if catalog == nil {
		return AuthorizationDescriptor{}, bridgeError(ErrorScopeInvalid, "routing catalog is required")
	}
	channel, ok := catalog.Channel(channelID)
	if !ok || !channel.Enabled {
		return AuthorizationDescriptor{}, bridgeError(ErrorScopeInvalid, "browser cookie channel is missing or disabled")
	}
	template, ok := catalog.RouteTemplate(channel.RouteTemplateID)
	if !ok || !catalog.TemplateEnabled(channel.RouteTemplateID) || !catalog.TemplateTrusted(channel.RouteTemplateID) {
		return AuthorizationDescriptor{}, bridgeError(ErrorScopeInvalid, "browser cookie route template is unavailable or untrusted")
	}
	auth := template.Auth
	if auth.Kind != "browser_cookie" || !auth.Required || auth.Browser != "chrome" || len(auth.PermissionOrigins) != 1 || auth.CookieScope == nil {
		return AuthorizationDescriptor{}, bridgeError(ErrorScopeInvalid, "route template does not declare one trusted Chrome cookie scope")
	}
	descriptor := AuthorizationDescriptor{
		LoginURL:                auth.LoginURL,
		PermissionOriginPattern: auth.PermissionOrigins[0],
		CookieScope: CookieScope{
			URL:            auth.CookieScope.URL,
			AllowedDomains: append([]string(nil), auth.CookieScope.AllowedDomains...),
			Names:          append([]string(nil), auth.CookieScope.Names...),
			Store:          auth.CookieScope.Store,
			Partitions:     append([]string(nil), auth.CookieScope.Partitions...),
		},
	}
	request := ReadCookiesRequest{RequestID: "authorization_validation", ChannelID: channel.ID, PermissionOriginPattern: descriptor.PermissionOriginPattern, CookieScope: descriptor.CookieScope}
	if err := validateReadCookiesRequest(request); err != nil {
		return AuthorizationDescriptor{}, err
	}
	return descriptor, nil
}
