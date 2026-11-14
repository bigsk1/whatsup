//go:build ts_omit_useproxy && ts_omit_portmapper && ts_omit_captiveportal

package transport

// The supported build tags prevent upstream networking from using inherited
// proxies, opening router mappings or probing captive-portal endpoints.
const networkPrivacyReady = true
