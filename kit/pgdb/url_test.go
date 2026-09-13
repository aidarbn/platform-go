package pgdb_test

import "net/url"

// pgxConfigFor returns the same connection URL pointed at another database.
func pgxConfigFor(raw, database string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = "/" + database
	return u.String(), nil
}
