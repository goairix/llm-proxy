package responses

type FlushWriter interface {
	Write([]byte) (int, error)
	Flush()
}
