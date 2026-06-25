package azure

// cosmos.go — live Cosmos export. Structure comes from `az` (control plane:
// which databases and collections exist); the documents come from `mongoexport`
// (data plane: the Mongo wire protocol az can't speak). Only the Mongo API is
// exported — Cosmos SQL/Gremlin/Cassandra refuse cleanly and stay on Azure. A
// missing `mongoexport` refuses with the install hint rather than a silent empty
// export: the snapshot is honest about what it couldn't take.
//
// mongoexport is MongoDB's official tool and Azure's own recommended way to get
// Cosmos Mongo data out, so the shell-out matches the tool's "use the vendor's
// own CLIs" posture (like `az`). The connection string carries the account key,
// so it is never logged.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// cosmosCollections exports every collection in a Cosmos Mongo-API account as
// collectionName→documents (raw JSONL from mongoexport, kept verbatim — the
// transform stage handles any id remap). Refuses cleanly on a non-Mongo API or a
// missing mongoexport.
func (p azProvider) cosmosCollections(account string) (map[string][]json.RawMessage, error) {
	if err := p.requireCosmosMongo(account); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("mongoexport"); err != nil {
		return nil, fmt.Errorf("exporting Cosmos Mongo needs `mongoexport` (MongoDB Database Tools) — install it (brew install mongodb-database-tools, or https://www.mongodb.com/try/download/database-tools), then re-run")
	}
	conn, err := p.cosmosMongoConnString(account)
	if err != nil {
		return nil, err
	}
	dbs, err := p.mongoDatabases(account)
	if err != nil {
		return nil, err
	}

	out := map[string][]json.RawMessage{}
	single := len(dbs) == 1
	for _, db := range dbs {
		colls, err := p.mongoCollections(account, db)
		if err != nil {
			return nil, err
		}
		for _, coll := range colls {
			docs, err := mongoexportJSONL(conn, db, coll)
			if err != nil {
				return nil, fmt.Errorf("mongoexport %s.%s: %w", db, coll, err)
			}
			// Drift NoSQL is a flat collection namespace. Preserve the bare
			// collection name in the common single-database account; disambiguate
			// with the db only when more than one database would collide.
			key := coll
			if !single {
				key = db + "-" + coll
			}
			out[key] = docs
		}
	}
	return out, nil
}

// requireCosmosMongo confirms the account speaks the Mongo API; anything else
// (SQL/Core, Gremlin, Cassandra, Table) refuses — only Mongo maps cleanly to
// Drift NoSQL today, and only it can be read with mongoexport.
func (p azProvider) requireCosmosMongo(account string) error {
	var acct struct {
		Kind         string `json:"kind"`
		Capabilities []struct {
			Name string `json:"name"`
		} `json:"capabilities"`
	}
	if err := p.c.runJSON([]string{"cosmosdb", "show", "-g", p.rg, "-n", account}, &acct); err != nil {
		return fmt.Errorf("reading cosmos account %s: %w", account, err)
	}
	if strings.EqualFold(acct.Kind, "MongoDB") {
		return nil
	}
	for _, c := range acct.Capabilities {
		if strings.EqualFold(c.Name, "EnableMongo") {
			return nil
		}
	}
	return fmt.Errorf("cosmos account %s is the %q API — only the Mongo API is exported today; SQL/Gremlin/Cassandra/Table stay on Azure", account, acct.Kind)
}

// cosmosMongoConnString fetches the primary Mongo connection string (carries the
// account key — never logged).
func (p azProvider) cosmosMongoConnString(account string) (string, error) {
	var keys struct {
		ConnectionStrings []struct {
			ConnectionString string `json:"connectionString"`
			Description      string `json:"description"`
		} `json:"connectionStrings"`
	}
	if err := p.c.runJSON([]string{"cosmosdb", "keys", "list", "-g", p.rg, "-n", account, "--type", "connection-strings"}, &keys); err != nil {
		return "", fmt.Errorf("reading cosmos connection strings: %w", err)
	}
	for _, cs := range keys.ConnectionStrings {
		if strings.Contains(strings.ToLower(cs.Description), "primary") && strings.Contains(strings.ToLower(cs.Description), "mongo") {
			return cs.ConnectionString, nil
		}
	}
	// Fall back to the first available string rather than failing outright.
	if len(keys.ConnectionStrings) > 0 {
		return keys.ConnectionStrings[0].ConnectionString, nil
	}
	return "", fmt.Errorf("no Mongo connection string on cosmos account %s", account)
}

// mongoDatabases / mongoCollections read the account's structure from the az
// control plane (no data, no key on the command line).
func (p azProvider) mongoDatabases(account string) ([]string, error) {
	var dbs []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"cosmosdb", "mongodb", "database", "list", "-g", p.rg, "--account-name", account}, &dbs); err != nil {
		return nil, fmt.Errorf("listing cosmos databases: %w", err)
	}
	names := make([]string, 0, len(dbs))
	for _, d := range dbs {
		names = append(names, d.Name)
	}
	return names, nil
}

func (p azProvider) mongoCollections(account, db string) ([]string, error) {
	var colls []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"cosmosdb", "mongodb", "collection", "list", "-g", p.rg, "--account-name", account, "--database-name", db}, &colls); err != nil {
		return nil, fmt.Errorf("listing collections in %s: %w", db, err)
	}
	names := make([]string, 0, len(colls))
	for _, c := range colls {
		names = append(names, c.Name)
	}
	return names, nil
}

// mongoexportJSONL runs mongoexport for one collection and returns its documents,
// one json.RawMessage per output line (mongoexport emits JSONL). The uri carries
// the account key and is never printed.
func mongoexportJSONL(uri, db, coll string) ([]json.RawMessage, error) {
	cmd := exec.Command("mongoexport", "--uri", uri, "--db", db, "--collection", coll, "--quiet") // #nosec G204 -- db/coll are az-listed names; uri is the account's own string
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var docs []json.RawMessage
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		docs = append(docs, append(json.RawMessage(nil), line...))
	}
	return docs, nil
}
