package influxdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/influxdb/influxdb/client"
)

var (
	// ErrServerOpen is returned when opening an already open server.
	ErrServerOpen = errors.New("server already open")

	// ErrServerClosed is returned when closing an already closed server.
	ErrServerClosed = errors.New("server already closed")

	// ErrPathRequired is returned when opening a server without a path.
	ErrPathRequired = errors.New("path required")

	// ErrUnableToJoin is returned when a server cannot join a cluster.
	ErrUnableToJoin = errors.New("unable to join")

	// ErrDataNodeURLRequired is returned when creating a data node without a URL.
	ErrDataNodeURLRequired = errors.New("data node url required")

	// ErrDataNodeExists is returned when creating a duplicate data node.
	ErrDataNodeExists = errors.New("data node exists")

	// ErrDataNodeNotFound is returned when dropping a non-existent data node.
	ErrDataNodeNotFound = errors.New("data node not found")

	// ErrDataNodeRequired is returned when using a blank data node id.
	ErrDataNodeRequired = errors.New("data node required")

	// ErrDatabaseNameRequired is returned when creating a database without a name.
	ErrDatabaseNameRequired = errors.New("database name required")

	// ErrDatabaseExists is returned when creating a duplicate database.
	ErrDatabaseExists = errors.New("database exists")

	// ErrDatabaseNotFound is returned when dropping a non-existent database.
	ErrDatabaseNotFound = errors.New("database not found")

	// ErrDatabaseRequired is returned when using a blank database name.
	ErrDatabaseRequired = errors.New("database required")

	// ErrClusterAdminExists is returned when creating a duplicate admin.
	ErrClusterAdminExists = errors.New("cluster admin exists")

	// ErrClusterAdminNotFound is returned when deleting a non-existent admin.
	ErrClusterAdminNotFound = errors.New("cluster admin not found")

	// ErrUserExists is returned when creating a duplicate user.
	ErrUserExists = errors.New("user exists")

	// ErrUserNotFound is returned when deleting a non-existent user.
	ErrUserNotFound = errors.New("user not found")

	// ErrUsernameRequired is returned when using a blank username.
	ErrUsernameRequired = errors.New("username required")

	// ErrInvalidUsername is returned when using a username with invalid characters.
	ErrInvalidUsername = errors.New("invalid username")

	// ErrRetentionPolicyExists is returned when creating a duplicate shard space.
	ErrRetentionPolicyExists = errors.New("retention policy exists")

	// ErrRetentionPolicyNotFound is returned when deleting a non-existent shard space.
	ErrRetentionPolicyNotFound = errors.New("retention policy not found")

	// ErrRetentionPolicyNameRequired is returned using a blank shard space name.
	ErrRetentionPolicyNameRequired = errors.New("retention policy name required")

	// ErrDefaultRetentionPolicyNotFound is returned when using the default
	// policy on a database but the default has not been set.
	ErrDefaultRetentionPolicyNotFound = errors.New("default retention policy not found")

	// ErrShardNotFound is returned writing to a non-existent shard.
	ErrShardNotFound = errors.New("shard not found")

	// ErrReadAccessDenied is returned when a user attempts to read
	// data that he or she does not have permission to read.
	ErrReadAccessDenied = errors.New("read access denied")

	// ErrReadWritePermissionsRequired is returned when required read/write permissions aren't provided.
	ErrReadWritePermissionsRequired = errors.New("read/write permissions required")

	// ErrInvalidQuery is returned when executing an unknown query type.
	ErrInvalidQuery = errors.New("invalid query")

	// ErrMeasurementNameRequired is returned when a point does not contain a name.
	ErrMeasurementNameRequired = errors.New("measurement name required")

	// ErrMeasurementNotFound is returned when a measurement does not exist.
	ErrMeasurementNotFound = errors.New("measurement not found")

	// ErrValuesRequired is returned when a point does not any values
	ErrValuesRequired = errors.New("values required")

	// ErrFieldOverflow is returned when too many fields are created on a measurement.
	ErrFieldOverflow = errors.New("field overflow")

	// ErrSeriesNotFound is returned when looking up a non-existent series by database, name and tags
	ErrSeriesNotFound = errors.New("series not found")

	// ErrSeriesExists is returned when attempting to set the id of a series by database, name and tags that already exists
	ErrSeriesExists = errors.New("series already exists")

	// ErrNotExecuted is returned when a statement is not executed in a query.
	// This can occur when a previous statement in the same query has errored.
	ErrNotExecuted = errors.New("not executed")

	// ErrInvalidGrantRevoke is returned when a statement requests an invalid
	// privilege for a user on the cluster or a database.
	ErrInvalidGrantRevoke = errors.New("invalid privilege requested")

	// ErrContinuousQueryExists is returned when creating a duplicate continuous query.
	ErrContinuousQueryExists = errors.New("continuous query already exists")
)

// BatchPoints is used to send batched data in a single write.
type BatchPoints struct {
	Points          []client.Point    `json:"points"`
	Database        string            `json:"database"`
	RetentionPolicy string            `json:"retentionPolicy"`
	Tags            map[string]string `json:"tags"`
	Timestamp       time.Time         `json:"timestamp"`
	Precision       string            `json:"precision"`
}

// UnmarshalJSON decodes the data into the BatchPoints struct
func (bp *BatchPoints) UnmarshalJSON(b []byte) error {
	var normal struct {
		Points          []client.Point    `json:"points"`
		Database        string            `json:"database"`
		RetentionPolicy string            `json:"retentionPolicy"`
		Tags            map[string]string `json:"tags"`
		Timestamp       time.Time         `json:"timestamp"`
		Precision       string            `json:"precision"`
	}
	var epoch struct {
		Points          []client.Point    `json:"points"`
		Database        string            `json:"database"`
		RetentionPolicy string            `json:"retentionPolicy"`
		Tags            map[string]string `json:"tags"`
		Timestamp       *int64            `json:"timestamp"`
		Precision       string            `json:"precision"`
	}

	if err := func() error {
		var err error
		if err = json.Unmarshal(b, &epoch); err != nil {
			return err
		}
		// Convert from epoch to time.Time
		var ts time.Time
		if epoch.Timestamp != nil {
			ts, err = client.EpochToTime(*epoch.Timestamp, epoch.Precision)
			if err != nil {
				return err
			}
		}
		bp.Points = epoch.Points
		bp.Database = epoch.Database
		bp.RetentionPolicy = epoch.RetentionPolicy
		bp.Tags = epoch.Tags
		bp.Timestamp = ts
		bp.Precision = epoch.Precision
		return nil
	}(); err == nil {
		return nil
	}

	if err := json.Unmarshal(b, &normal); err != nil {
		return err
	}
	normal.Timestamp = client.SetPrecision(normal.Timestamp, normal.Precision)
	bp.Points = normal.Points
	bp.Database = normal.Database
	bp.RetentionPolicy = normal.RetentionPolicy
	bp.Tags = normal.Tags
	bp.Timestamp = normal.Timestamp
	bp.Precision = normal.Precision

	return nil
}

// NormalizeBatchPoints returns a slice of Points, created by populating individual
// points within the batch, which do not have timestamps or tags, with the top-level
// values.
func NormalizeBatchPoints(bp BatchPoints) ([]Point, error) {
	points := []Point{}
	for _, p := range bp.Points {
		if p.Timestamp.Time().IsZero() {
			p.Timestamp = client.Timestamp(bp.Timestamp)
		}
		if p.Precision == "" && bp.Precision != "" {
			p.Precision = bp.Precision
		}
		p.Timestamp = client.Timestamp(client.SetPrecision(p.Timestamp.Time(), p.Precision))
		if len(bp.Tags) > 0 {
			if p.Tags == nil {
				p.Tags = make(map[string]string)
			}
			for k := range bp.Tags {
				if p.Tags[k] == "" {
					p.Tags[k] = bp.Tags[k]
				}
			}
		}
		// Need to convert from a client.Point to a influxdb.Point
		points = append(points, Point{
			Name:      p.Name,
			Tags:      p.Tags,
			Timestamp: p.Timestamp.Time(),
			Values:    p.Values,
		})
	}

	return points, nil
}

// ErrAuthorize represents an authorization error.
type ErrAuthorize struct {
	text string
}

// Error returns the text of the error.
func (e ErrAuthorize) Error() string {
	return e.text
}

// authorize satisfies isAuthorizationError
func (ErrAuthorize) authorize() {}

func isAuthorizationError(err error) bool {
	type authorize interface {
		authorize()
	}
	_, ok := err.(authorize)
	return ok
}

// mustMarshal encodes a value to JSON.
// This will panic if an error occurs. This should only be used internally when
// an invalid marshal will cause corruption and a panic is appropriate.
func mustMarshalJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("marshal: " + err.Error())
	}
	return b
}

// mustUnmarshalJSON decodes a value from JSON.
// This will panic if an error occurs. This should only be used internally when
// an invalid unmarshal will cause corruption and a panic is appropriate.
func mustUnmarshalJSON(b []byte, v interface{}) {
	if err := json.Unmarshal(b, v); err != nil {
		panic("unmarshal: " + err.Error())
	}
}

// assert will panic with a given formatted message if the given condition is false.
func assert(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assert failed: "+msg, v...))
	}
}

func warn(v ...interface{})              { fmt.Fprintln(os.Stderr, v...) }
func warnf(msg string, v ...interface{}) { fmt.Fprintf(os.Stderr, msg+"\n", v...) }
