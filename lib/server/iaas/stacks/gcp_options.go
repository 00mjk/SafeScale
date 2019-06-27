package stacks

// Google cloud platform configuration
type GCPConfiguration struct {
	Type string `json:"type" validate:"required"`
	ProjectId      string `json:"project_id"`
	PrivateKeyId     string `json:"private_key_id"`
	PrivateKey      string `json:"private_key"`
	ClientEmail string   `json:"client_email"`
	ClientId string   `json:"client_id"`
	AuthUri string   `json:"auth_uri"`
	TokenUri string   `json:"token_uri"`
	AuthProvider string   `json:"auth_provider_x509_cert_url"`
	ClientCert string   `json:"client_x509_cert_url"`
	Region string `json:"-"`
	Zone string `json:"-"`
}
