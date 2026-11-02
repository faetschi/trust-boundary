//go:build linux

package main

import (
	"context"
	"testing"

	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/productsetup"
)

func TestProductCommandHostRequiresCanonicalInstalledInputs(t *testing.T) {
	cfg := productServiceConfig{
		RegistrationPath:          productsetup.RegistrationPath,
		RegistrationSignaturePath: productsetup.RegistrationSignaturePath,
		RegistrationPublicKeyPath: productsetup.RegistrationPublicKeyPath,
	}
	for _, field := range []string{"registration", "signature", "key"} {
		changed := cfg
		switch field {
		case "registration":
			changed.RegistrationPath = "/etc/tbound/registration.json"
		case "signature":
			changed.RegistrationSignaturePath += ".other"
		case "key":
			changed.RegistrationPublicKeyPath += ".other"
		}
		if _, err := loadProductCommandHost(context.Background(), changed, &productTurnEnv{}); err == nil {
			t.Errorf("alternate %s path accepted", field)
		}
	}
	if _, err := loadProductCommandHost(context.Background(), cfg, nil); err == nil {
		t.Fatal("nil product environment accepted")
	}
}

func TestProductCommandHostStaticValidationGrantsNoAuthority(t *testing.T) {
	if _, _, err := validateProductCommandInputs(productServiceConfig{}, productsession.Registration{}, productCommandInputs{}); err == nil {
		t.Fatal("empty unsigned candidate accepted")
	}
	var host piruntime.D09ProductCommandHost
	if err := host.Revalidate(context.Background()); err == nil {
		t.Fatal("zero product host passed revalidation")
	}
}
