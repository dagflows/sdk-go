package runtime

// ParseExpansion is the measured memory multiplier from JSON bytes to decoded Go values.
// Used to guard against memory exhaustion by refusing oversized reference payloads before loading.
const ParseExpansion = 12
