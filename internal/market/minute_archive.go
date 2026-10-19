package market

// MinuteArchiveLimit bounds a twenty-year research dataset, not a provider page.
const MinuteArchiveLimit = 20 * 366 * 48

func (h MinuteHistory) ValidateArchive() error { return h.validateLimit(MinuteArchiveLimit) }
