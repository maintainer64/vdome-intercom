package provider

import (
	"context"
	"vdome/internal/domain"
)

// Intentionally no guessed URLs, OAuth client IDs or presumed JSON schemas.
type Unconfigured struct{}

func (Unconfigured) Name() string { return "unconfigured" }
func (Unconfigured) Login(context.Context, domain.Credentials) (domain.Login, error) {
	return domain.Login{}, domain.ErrUnconfigured
}
func (Unconfigured) Devices(context.Context, *domain.Auth) ([]domain.Device, error) {
	return nil, domain.ErrUnconfigured
}
func (Unconfigured) Calls(context.Context, *domain.Auth) ([]domain.Call, error) {
	return nil, domain.ErrUnconfigured
}
func (Unconfigured) Open(context.Context, *domain.Auth, string, string) error {
	return domain.ErrUnconfigured
}
func (Unconfigured) Snapshot(context.Context, *domain.Auth, string) ([]byte, string, error) {
	return nil, "", domain.ErrUnconfigured
}
