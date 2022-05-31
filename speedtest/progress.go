package speedtest

// WriterFunc allows using a func as a io.Writer
type WriterFunc func(p []byte) (n int, err error)

// Write implements io.Write
func (fn WriterFunc) Write(p []byte) (n int, err error) {
	return fn(p)
}

// ProgressUpdater keeps track of progress update
type ProgressUpdater interface {
	Update(nbytes uint64)
}

// ProgressUpdaterFunc allows using a func as a ProgressHandler
type ProgressUpdaterFunc func(bytes uint64)

// Update implements ProgressHandler
func (fn ProgressUpdaterFunc) Update(nbytes uint64) {
	fn(nbytes)
}
