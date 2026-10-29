package productsession

// Product defaults never share a path with the thesis service.
const (
	DefaultRegistrationPath          = "/etc/tbound-product/registration.json"
	DefaultRegistrationSignaturePath = DefaultRegistrationPath + ".sig"
	DefaultRegistrationPublicKeyPath = "/etc/tbound-product/registration.pub"
	DefaultCatalogPath               = "/var/lib/tbound-product/catalog.jsonl"
	DefaultTurnStateRoot             = "/var/lib/tbound-product/turns/"
	DefaultServiceSocketPath         = "/run/tbound-product/product.sock"
)
