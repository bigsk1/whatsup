package attachmentstore

// Usage is the contact's logical quota charge, including full object
// reservations and retained replay metadata. It is not filesystem allocation.
type Usage struct {
	UsedBytes  uint64
	QuotaBytes uint64
}

// ChargedUsage applies the same fixed budget that metadata mutations enforce.
// The authenticated metadata provider supplies the aggregate; no object names,
// keys, units or content need to cross this read-only boundary.
func ChargedUsage(used uint64) (Usage, error) {
	if used > QuotaBytes {
		return Usage{}, ErrIntegrity
	}
	return Usage{UsedBytes: used, QuotaBytes: QuotaBytes}, nil
}
