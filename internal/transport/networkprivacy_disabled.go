//go:build !ts_omit_useproxy || !ts_omit_portmapper || !ts_omit_captiveportal

package transport

// A plain/custom Go build must not silently weaken the reviewed outbound policy.
const networkPrivacyReady = false
