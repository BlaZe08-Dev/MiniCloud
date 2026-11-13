package worker

// The result-delivery backoff settings are unexported production constants. The external tests read
// them through these names so a row for that caller tracks the production values.
const (
	DeliveryBackoffBaseForTest = deliveryBackoffBase
	DeliveryBackoffMaxForTest  = deliveryBackoffMax
)
