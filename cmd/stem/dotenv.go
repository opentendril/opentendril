package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// dotenvWarning renders the operator warning for the result of loading a .env
// file, or "" when nothing needs saying.
//
// The two failures are not the same thing. No .env at all is the ordinary
// unconfigured case: most installations never have one, and saying so on every
// start would be noise. A .env that exists and cannot be read or parsed is the
// opposite — every value an operator put in it is silently absent, so a model
// pin, a provider choice, or an API key appears to be configured and is not.
// Discarding that error makes a misconfigured installation indistinguishable
// from a correctly configured one until something downstream behaves oddly for
// reasons nothing explains.
//
// dir is reported because godotenv resolves ".env" against the working
// directory, which for a service is set by the unit rather than by whoever is
// reading the message.
func dotenvWarning(err error, dir string) string {
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	location := ".env"
	if dir != "" {
		location = dir + "/.env"
	}
	return fmt.Sprintf(
		"⚠️  %s exists but was not loaded: %v\n"+
			"   Nothing in it is in effect — provider, model and key settings there are being ignored.\n",
		location, err)
}

// workingDirForReport names the directory godotenv resolved ".env" against, or
// "" when it cannot be determined. A failure here must not suppress the warning
// itself: the path is context, the warning is the point.
func workingDirForReport() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}
