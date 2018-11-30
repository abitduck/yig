package tidbclient

import (
	"database/sql"
	"fmt"
	. "github.com/journeymidnight/yig/meta/types"
	"math"
	"strings"
	"time"
)

//gc
func (t *TidbClient) PutObjectToGarbageCollection(object *Object) error {
	o := GarbageCollectionFromObject(object)
	var hasPart bool
	if len(o.Parts) > 0 {
		hasPart = true
	}
	mtime := o.MTime.Format(TIME_LAYOUT_TIDB)
	version := math.MaxUint64 - uint64(object.LastModifiedTime.UnixNano())
	sqltext := fmt.Sprintf("insert ignore into gc(bucketname,objectname,version,location,pool,objectid,status,mtime,part,triedtimes) "+
		"values('%s','%s',%d,'%s','%s','%s','%s','%s',%t,%d)",
		o.BucketName, o.ObjectName, version, o.Location, o.Pool, o.ObjectId, o.Status, mtime, hasPart, o.TriedTimes)
	tx, err := t.Client.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	_, err = tx.Exec(sqltext)
	if err != nil {
		return err
	}
	for _, p := range object.Parts {
		psql, args := p.GetCreateGcSql(o.BucketName, o.ObjectName, version)
		_, err = tx.Exec(psql, args...)
		if err != nil {
			return err
		}
	}

	return nil
}

func (t *TidbClient) ScanGarbageCollection(limit int, startRowKey string) (gcs []GarbageCollection, err error) {
	var count int
	var sqltext string
	if startRowKey == "" {
		sqltext = fmt.Sprintf("select bucketname,objectname,version from gc  order by bucketname,objectname,version limit %d", limit)
	} else {
		s := strings.Split(startRowKey, ObjectNameSeparator)
		bucketname := s[0]
		objectname := s[1]
		version := s[2]
		sqltext = fmt.Sprintf("select bucketname,objectname,version from gc where bucketname>'%s' or (bucketname='%s' and objectname>'%s') or (bucketname='%s' and objectname='%s' and version >= %s) limit %d", bucketname, bucketname, objectname, bucketname, objectname, version, limit)
	}
	rows, err := t.Client.Query(sqltext)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var b, o, v string
		err = rows.Scan(
			&b,
			&o,
			&v,
		)
		var gc GarbageCollection = GarbageCollection{}
		gc, err = t.GetGarbageCollection(b, o, v)
		if err != nil {
			return
		}
		gcs = append(gcs, gc)
		count += 1
		if count >= limit {
			break
		}
	}
	return
}

func (t *TidbClient) RemoveGarbageCollection(garbage GarbageCollection) error {
	tx, err := t.Client.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	version := strings.Split(garbage.Rowkey, ObjectNameSeparator)[2]
	sqltext := fmt.Sprintf("delete from gc where bucketname='%s' and objectname='%s' and version=%s", garbage.BucketName, garbage.ObjectName, version)
	_, err = tx.Exec(sqltext)
	if err != nil {
		return err
	}
	if len(garbage.Parts) > 0 {
		sqltext := fmt.Sprintf("delete from gcpart where bucketname='%s' and objectname='%s' and version=%s", garbage.BucketName, garbage.ObjectName, version)
		_, err := tx.Exec(sqltext)
		if err != nil {
			return err
		}
	}
	return nil
}

//util func
func (t *TidbClient) GetGarbageCollection(bucketName, objectName, version string) (gc GarbageCollection, err error) {
	sqltext := fmt.Sprintf("select bucketname,objectname,version,location,pool,objectid,status,mtime,part,triedtimes from gc where bucketname='%s' and objectname='%s' and version='%s'", bucketName, objectName, version)
	var hasPart bool
	var mtime string
	var v string
	err = t.Client.QueryRow(sqltext).Scan(
		&gc.BucketName,
		&gc.ObjectName,
		&v,
		&gc.Location,
		&gc.Pool,
		&gc.ObjectId,
		&gc.Status,
		&mtime,
		&hasPart,
		&gc.TriedTimes,
	)
	gc.MTime, err = time.Parse(TIME_LAYOUT_TIDB, mtime)
	if err != nil {
		return
	}
	gc.Rowkey = gc.BucketName + ObjectNameSeparator + gc.ObjectName + ObjectNameSeparator + v
	if hasPart {
		var p map[int]*Part
		p, err = getGcParts(bucketName, objectName, version, t.Client)
		if err != nil {
			return
		}
		gc.Parts = p
	}
	return
}

func getGcParts(bucketname, objectname, version string, cli *sql.DB) (parts map[int]*Part, err error) {
	parts = make(map[int]*Part)
	sqltext := fmt.Sprintf("select partnumber,size,objectid,offset,etag,lastmodified,initializationvector from gcpart where bucketname='%s' and objectname='%s' and version='%s'", bucketname, objectname, version)
	rows, err := cli.Query(sqltext)
	defer rows.Close()
	if err != nil {
		return
	}
	for rows.Next() {
		var p *Part = &Part{}
		err = rows.Scan(
			&p.PartNumber,
			&p.Size,
			&p.ObjectId,
			&p.Offset,
			&p.Etag,
			&p.LastModified,
			&p.InitializationVector,
		)
		parts[p.PartNumber] = p
	}
	return
}

func GarbageCollectionFromObject(o *Object) (gc GarbageCollection) {
	gc.BucketName = o.BucketName
	gc.ObjectName = o.Name
	gc.Location = o.Location
	gc.Pool = o.Pool
	gc.ObjectId = o.ObjectId
	gc.Status = "Pending"
	gc.MTime = time.Now().UTC()
	gc.Parts = o.Parts
	gc.TriedTimes = 0
	return
}
