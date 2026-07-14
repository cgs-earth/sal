package query

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/cgs-earth/sal/pkg"
)

type QueryCmd struct {
	Info string `help:"Retrieve quick info about the data product. Options: head, snapshots, column-stats properties" default:"head"`
}

func Run(cmd *QueryCmd) error {
	warehouse, err := pkg.SalDataDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(warehouse)
	if err != nil {
		return fmt.Errorf("failed to read SAL data directory: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("no data has been built yet; run `sal build` to build a data product first")
	}

	namespace, err := pkg.GitProjectName()
	if err != nil {
		return err
	}

	tablePath := joinRemote(warehouse, namespace, "triples")
	escapedTablePath := strings.ReplaceAll(tablePath, "'", "''")

	infoQuery, err := queryForInfo(cmd.Info, escapedTablePath)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "sal-duckdb-*.sql")
	if err != nil {
		return fmt.Errorf("failed to create duckdb init file: %w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil {
			slog.Error(err.Error())
		}
	}()

	_, err = fmt.Fprintf(tmp, `
INSTALL iceberg;
LOAD iceberg;

CREATE OR REPLACE VIEW triples AS
SELECT *
FROM iceberg_scan('%s', allow_moved_paths = true);

.mode box

%s;

.print ''
.print 'Connected to Iceberg table as view: triples'
.print 'You can now query it, e.g.:'
.print '  SELECT * FROM triples LIMIT 10;'
.print ''
`, escapedTablePath, infoQuery)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write duckdb init file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close duckdb init file: %w", err)
	}

	duck := exec.Command("duckdb", "-init", tmp.Name())
	duck.Stdin = os.Stdin
	duck.Stdout = os.Stdout
	duck.Stderr = os.Stderr

	if err := duck.Run(); err != nil {
		return fmt.Errorf("failed to open duckdb shell: %w", err)
	}

	return nil
}

func queryForInfo(info string, escapedTablePath string) (string, error) {
	switch info {
	case "", "head":
		return "SELECT * FROM triples LIMIT 20", nil
	case "properties":
		return fmt.Sprintf(`
WITH latest_metadata AS (
	SELECT
		filename,
		content::JSON AS metadata_json
	FROM read_text('%s/metadata/*.metadata.json')
	ORDER BY regexp_extract(filename, 'v([0-9]+)\.metadata\.json', 1)::BIGINT DESC
	LIMIT 1
)
SELECT
	prop.key,
	json_extract_string(prop.value, '$') AS value
FROM latest_metadata,
json_each(json_extract(metadata_json, '$.properties')) AS prop
ORDER BY prop.key`, escapedTablePath), nil
	case "snapshots":
		return fmt.Sprintf("SELECT * FROM iceberg_snapshots('%s')", escapedTablePath), nil
	case "column-stats":
		return fmt.Sprintf("SELECT * FROM iceberg_column_stats('%s')", escapedTablePath), nil
	default:
		return "", fmt.Errorf("unknown info option %q; expected one of: head, properties, snapshots, column-stats", info)
	}
}

func joinRemote(base string, parts ...string) string {
	joined := path.Join(parts...)
	if joined == "." {
		return strings.TrimSuffix(base, "/")
	}
	return strings.TrimSuffix(base, "/") + "/" + joined
}
