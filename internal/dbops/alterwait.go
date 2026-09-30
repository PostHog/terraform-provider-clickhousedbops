package dbops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

// RunningMutation is a mutation that an ALTER started and that had not finished when the
// provider stopped watching it.
type RunningMutation struct {
	ID        string
	Command   string
	PartsToDo uint64
}

// ALTERs run with alter_sync = 0, so that a statement never waits for a mutation to rewrite
// data. Waiting is done here instead, in short queries: until every replica has the new
// metadata, then briefly for the mutations the ALTER started, to catch one that fails at once.
var (
	alterPollInterval    = time.Second
	metadataWaitTimeout  = 2 * time.Minute
	mutationWatchTimeout = 10 * time.Second
)

// waitForAlter returns after the ALTER of database.name, started at server time startedAt,
// reached every replica's metadata. It returns the mutations the ALTER started that are still
// running, and an error for one that failed.
func (i *impl) waitForAlter(ctx context.Context, database string, name string, startedAt uint64) ([]RunningMutation, error) {
	if err := i.waitForReplicaMetadata(ctx, database, name); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(mutationWatchTimeout)
	for {
		mutations, err := i.mutationsSince(ctx, database, name, startedAt)
		if err != nil {
			return nil, err
		}
		running := make([]RunningMutation, 0, len(mutations))
		for _, mutation := range mutations {
			if mutation.failReason != "" {
				failing := FailingMutation{ID: mutation.ID, Command: mutation.Command, Reason: mutation.failReason}
				return nil, errors.Errorf("mutation %s (%s) of %s.%s fails: %s. The metadata change is already applied; fix the cause, or stop the mutation with %s",
					mutation.ID, mutation.Command, database, name, mutation.failReason, failing.KillHint(database, name))
			}
			if !mutation.done {
				running = append(running, mutation.RunningMutation)
			}
		}
		if len(running) == 0 || time.Now().After(deadline) {
			return running, nil
		}
		if err := sleepContext(ctx, alterPollInterval); err != nil {
			return nil, err
		}
	}
}

// waitForReplicaMetadata waits until every replica of a Replicated table has applied the
// table's latest metadata version from Keeper. Other tables return at once.
func (i *impl) waitForReplicaMetadata(ctx context.Context, database string, name string) error {
	zookeeperPath, err := i.zookeeperPath(ctx, database, name)
	if err != nil || zookeeperPath == "" {
		return err
	}

	deadline := time.Now().Add(metadataWaitTimeout)
	for {
		target, versions, err := i.replicaMetadataVersions(ctx, zookeeperPath)
		if err != nil {
			return err
		}
		var behind []string
		for replica, version := range versions {
			if version < target {
				behind = append(behind, fmt.Sprintf("%s (version %d)", replica, version))
			}
		}
		if len(behind) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			sort.Strings(behind)
			return errors.Errorf("replicas of %s.%s did not apply metadata version %d within %s: %s",
				database, name, target, metadataWaitTimeout, strings.Join(behind, ", "))
		}
		if err := sleepContext(ctx, alterPollInterval); err != nil {
			return err
		}
	}
}

func (i *impl) zookeeperPath(ctx context.Context, database string, name string) (string, error) {
	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewField("zookeeper_path")},
		"system.replicas",
	).Where(querybuilder.WhereEquals("database", database), querybuilder.WhereEquals("table", name)).Build()
	if err != nil {
		return "", errors.WithMessage(err, "error building query")
	}

	path := ""
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		path, err = data.GetString("zookeeper_path")
		return err
	})
	if err != nil {
		return "", errors.WithMessage(err, "error reading the Keeper path of the table")
	}
	return path, nil
}

// replicaMetadataVersions returns the version of the table's shared metadata node in Keeper,
// which every ALTER increments, and the metadata version each replica has applied.
func (i *impl) replicaMetadataVersions(ctx context.Context, zookeeperPath string) (uint64, map[string]uint64, error) {
	targetSQL, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewRawField("toUInt64(version)", "version")},
		"system.zookeeper",
	).Where(querybuilder.WhereEquals("path", zookeeperPath), querybuilder.WhereEquals("name", "metadata")).Build()
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error building query")
	}
	var target uint64
	err = i.clickhouseClient.Select(ctx, targetSQL, func(data clickhouseclient.Row) error {
		target, err = data.GetUInt64("version")
		return err
	})
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error reading the table's metadata version from Keeper")
	}

	replicasSQL, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewField("name")},
		"system.zookeeper",
	).Where(querybuilder.WhereEquals("path", zookeeperPath+"/replicas")).Build()
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error building query")
	}
	var replicaPaths []string
	err = i.clickhouseClient.Select(ctx, replicasSQL, func(data clickhouseclient.Row) error {
		replica, err := data.GetString("name")
		if err != nil {
			return err
		}
		replicaPaths = append(replicaPaths, zookeeperPath+"/replicas/"+replica)
		return nil
	})
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error listing the table's replicas in Keeper")
	}
	if len(replicaPaths) == 0 {
		return target, map[string]uint64{}, nil
	}

	versionsSQL, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewField("path"), querybuilder.NewRawField("toUInt64(value)", "value")},
		"system.zookeeper",
	).Where(querybuilder.WhereIn("path", replicaPaths), querybuilder.WhereEquals("name", "metadata_version")).Build()
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error building query")
	}
	versions := make(map[string]uint64, len(replicaPaths))
	err = i.clickhouseClient.Select(ctx, versionsSQL, func(data clickhouseclient.Row) error {
		path, err := data.GetString("path")
		if err != nil {
			return err
		}
		version, err := data.GetUInt64("value")
		versions[path[strings.LastIndex(path, "/")+1:]] = version
		return err
	})
	if err != nil {
		return 0, nil, errors.WithMessage(err, "error reading the replicas' metadata versions from Keeper")
	}
	return target, versions, nil
}

type mutationStatus struct {
	RunningMutation
	done       bool
	failReason string
}

func (i *impl) mutationsSince(ctx context.Context, database string, name string, startedAt uint64) ([]mutationStatus, error) {
	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{
			querybuilder.NewField("mutation_id"),
			querybuilder.NewField("command"),
			querybuilder.NewRawField("toUInt64(toUnixTimestamp(create_time))", "created"),
			querybuilder.NewRawField("toUInt64(parts_to_do)", "parts_to_do"),
			querybuilder.NewRawField("toUInt64(is_done)", "is_done"),
			querybuilder.NewField("latest_fail_reason"),
		},
		"system.mutations",
	).Where(querybuilder.WhereEquals("database", database), querybuilder.WhereEquals("table", name), querybuilder.WhereEquals("is_killed", 0)).Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	var mutations []mutationStatus
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		created, err := data.GetUInt64("created")
		if err != nil || created < startedAt {
			return err
		}
		mutation := mutationStatus{}
		if mutation.ID, err = data.GetString("mutation_id"); err != nil {
			return err
		}
		if mutation.Command, err = data.GetString("command"); err != nil {
			return err
		}
		if mutation.PartsToDo, err = data.GetUInt64("parts_to_do"); err != nil {
			return err
		}
		isDone, err := data.GetUInt64("is_done")
		if err != nil {
			return err
		}
		mutation.done = isDone == 1
		if mutation.failReason, err = data.GetString("latest_fail_reason"); err != nil {
			return err
		}
		mutations = append(mutations, mutation)
		return nil
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error reading mutations")
	}
	return mutations, nil
}

// FailingMutations returns the unfinished mutations of the table whose last attempt failed.
func (i *impl) FailingMutations(ctx context.Context, database string, name string) ([]FailingMutation, error) {
	mutations, err := i.mutationsSince(ctx, database, name, 0)
	if err != nil {
		return nil, err
	}
	var failing []FailingMutation
	for _, mutation := range mutations {
		if !mutation.done && mutation.failReason != "" {
			failing = append(failing, FailingMutation{ID: mutation.ID, Command: mutation.Command, Reason: mutation.failReason})
		}
	}
	return failing, nil
}

// FailingMutation is an unfinished mutation whose last attempt failed.
type FailingMutation struct {
	ID      string
	Command string
	Reason  string
}

// KillHint is the statement that stops the mutation.
func (m FailingMutation) KillHint(database string, name string) string {
	return fmt.Sprintf("KILL MUTATION WHERE database = '%s' AND table = '%s' AND mutation_id = '%s'", database, name, m.ID)
}

// serverNow returns the server's clock, which system.mutations.create_time is measured by.
func (i *impl) serverNow(ctx context.Context) (uint64, error) {
	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{querybuilder.NewRawField("toUInt64(toUnixTimestamp(now()))", "now")},
		"system.one",
	).Build()
	if err != nil {
		return 0, errors.WithMessage(err, "error building query")
	}
	var now uint64
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		now, err = data.GetUInt64("now")
		return err
	})
	if err != nil {
		return 0, errors.WithMessage(err, "error reading the server time")
	}
	return now, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
