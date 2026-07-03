package usage

type UsageExtractor interface {
	ExtractFromJSON(body []byte) (Usage, error)
}
