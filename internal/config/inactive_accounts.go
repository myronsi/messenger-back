package config

type InactiveAccounts struct {
	Deletion bool `env:"INACTIVE_ACCOUNT_DELETION" envDefault:"false"`
}
