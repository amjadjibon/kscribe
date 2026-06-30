package templates

import "github.com/amjadjibon/kscribe/public"

// Asset appends the content-hash version to a static asset URL so browsers
// can cache it indefinitely and still pick up changes on redeploy.
func Asset(path string) string {
	return path + "?v=" + public.Version
}
