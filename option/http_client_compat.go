package option

// HTTPClientOptions is accepted on the stable branch for configuration
// compatibility with newer cagedbird templates. The stable 1.13 runtime keeps
// using legacy download_detour/default transports and intentionally ignores
// these named client definitions.
type HTTPClientOptions struct {
	Tag string `json:"tag,omitempty"`
}
