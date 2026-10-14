package faultproxy

import (
	_ "embed"
	"encoding/json"
)

const UA1MailSupplement = "umir-ua1-mail-fixtures/0.1.0-unpublished"
const UA1MailBindingSHA256 = "c468cb825a3b9bfe01577485f47267f4765c95286ac08de367002e3e25f3df45"

//go:embed ua1.schema.json
var ua1Schema []byte

//go:embed ua1.binding.json
var ua1BindingBytes []byte

func validateUA1Mail(definition string, data []byte) error {
	var binding struct {
		Supplement   string `json:"supplement"`
		SchemaSHA256 string `json:"schemaSHA256"`
	}
	if SHA256(ua1BindingBytes) != UA1MailBindingSHA256 || json.Unmarshal(ua1BindingBytes, &binding) != nil || binding.Supplement != UA1MailSupplement || binding.SchemaSHA256 != SHA256(ua1Schema) || len(data) > 262144 {
		return ErrProtocol
	}
	return ValidateSchema(ua1Schema, "#/$defs/"+definition, data, 16)
}
