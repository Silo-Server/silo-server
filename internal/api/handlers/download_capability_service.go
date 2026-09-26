package handlers

// ProxyDeliveryAvailable reports the same configured delivery boundary used by
// the frozen capability endpoint; request handlers still authorize every target.
func (h *DownloadHandler) ProxyDeliveryAvailable() bool {
	if h == nil {
		return false
	}
	_, _, available := h.proxyTarget()
	return available
}
