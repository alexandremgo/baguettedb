package domain

// ServedInstance is a bdb serve running on a data directory: the URL it is reached at, and its process.
type ServedInstance struct {
	URL string
	PID int
}
