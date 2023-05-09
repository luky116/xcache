// Copyright 2016 CodisLabs. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package topom

import (
	"bytes"

	"github.com/BurntSushi/toml"

	"github.com/CodisLabs/codis/pkg/models"
	"github.com/CodisLabs/codis/pkg/utils/bytesize"
	"github.com/CodisLabs/codis/pkg/utils/errors"
	"github.com/CodisLabs/codis/pkg/utils/log"
	"github.com/CodisLabs/codis/pkg/utils/timesize"
)

const DefaultConfig = `
##################################################
#                                                #
#                  Codis-Dashboard               #
#                                                #
##################################################

# Set runtime.GOMAXPROCS to N, default is 1.
ncpu = 1

# Set path/name of daliy rotated log file.
log = "dashboard.log"

# Expire-log-days, 0 means never expire
expire_log_days = 30

# Set the log-level, should be INFO,WARN,DEBUG or ERROR, default is INFO.
log_level = "info"

# Set pidfile
pidfile = "dashboard.pid"

# Set mysql server (such as localhost:3306), fe will report codis node to mysql.
mysql_addr = ""
mysql_username = ""
mysql_password = ""
mysql_database = ""

# Set Codis Product Name/Auth.
product_name = "codis-demo"
product_auth = ""

# Set bind address for admin(rpc), tcp only.
admin_addr = "0.0.0.0:18080"

# Set influxdb server (such as http://localhost:8086), dashboard will report metrics to influxdb.
# Dashboard use another two dastbases to record more cmd delay info, database suffix is "_extend_1" and "_extend_2"
metrics_report_influxdb_server = ""
metrics_report_influxdb_period = "1s"
metrics_report_influxdb_username = ""
metrics_report_influxdb_password = ""
metrics_report_influxdb_database = ""

# Set arguments for data migration (only accept 'sync' & 'semi-async').
migration_method = "sync"
migration_parallel_slots = 100
migration_async_maxbulks = 200
migration_async_maxbytes = "32mb"
migration_async_numkeys = 500
migration_timeout = "30s"

# Set configs for redis sentinel.
sentinel_client_timeout = "10s"
sentinel_quorum = 2
sentinel_parallel_syncs = 1
sentinel_down_after = "15s"
sentinel_failover_timeout = "5m"
sentinel_notification_script = ""
sentinel_client_reconfig_script = ""

# master mysql to reload 
master_product = ""
master_mysql_addr = ""
master_mysql_username = ""
master_mysql_password = ""
master_mysql_database = ""
`

type Config struct {
	ConfigName string `toml:"-" json:"config_name"`

	CoordinatorName string `toml:"coordinator_name" json:"-"`
	CoordinatorAddr string `toml:"coordinator_addr" json:"-"`
	CoordinatorAuth string `toml:"coordinator_auth" json:"-"`

	AdminAddr string `toml:"admin_addr" json:"admin_addr"`

	HostAdmin string `toml:"-" json:"-"`

	ProductName string `toml:"product_name" json:"product_name"`
	ProductAuth string `toml:"product_auth" json:"-"`

	MetricsReportInfluxdbServer   string            `toml:"metrics_report_influxdb_server" json:"metrics_report_influxdb_server"`
	MetricsReportInfluxdbPeriod   timesize.Duration `toml:"metrics_report_influxdb_period" json:"metrics_report_influxdb_period"`
	MetricsReportInfluxdbUsername string            `toml:"metrics_report_influxdb_username" json:"metrics_report_influxdb_username"`
	MetricsReportInfluxdbPassword string            `toml:"metrics_report_influxdb_password" json:"-"`
	MetricsReportInfluxdbDatabase string            `toml:"metrics_report_influxdb_database" json:"metrics_report_influxdb_database"`

	MigrationMethod        string            `toml:"migration_method" json:"migration_method"`
	MigrationParallelSlots int               `toml:"migration_parallel_slots" json:"migration_parallel_slots"`
	MigrationAsyncMaxBulks int               `toml:"migration_async_maxbulks" json:"migration_async_maxbulks"`
	MigrationAsyncMaxBytes bytesize.Int64    `toml:"migration_async_maxbytes" json:"migration_async_maxbytes"`
	MigrationAsyncNumKeys  int               `toml:"migration_async_numkeys" json:"migration_async_numkeys"`
	MigrationTimeout       timesize.Duration `toml:"migration_timeout" json:"migration_timeout"`

	SentinelClientTimeout        timesize.Duration `toml:"sentinel_client_timeout" json:"sentinel_client_timeout"`
	SentinelQuorum               int               `toml:"sentinel_quorum" json:"sentinel_quorum"`
	SentinelParallelSyncs        int               `toml:"sentinel_parallel_syncs" json:"sentinel_parallel_syncs"`
	SentinelDownAfter            timesize.Duration `toml:"sentinel_down_after" json:"sentinel_down_after"`
	SentinelFailoverTimeout      timesize.Duration `toml:"sentinel_failover_timeout" json:"sentinel_failover_timeout"`
	SentinelNotificationScript   string            `toml:"sentinel_notification_script" json:"sentinel_notification_script"`
	SentinelClientReconfigScript string            `toml:"sentinel_client_reconfig_script" json:"sentinel_client_reconfig_script"`

	Ncpu          int    `toml:"ncpu"`
	Log           string `toml:"log"`
	ExpireLogDays int    `toml:"expire_log_days"`
	LogLevel      string `toml:"log_level"`
	PidFile       string `toml:"pidfile"`

	MysqlAddr     string `toml:"mysql_addr" json:"mysql_addr"`
	MysqlUsername string `toml:"mysql_username" json:"mysql_username"`
	MysqlPassword string `toml:"mysql_password" json:"-"`
	MysqlDatabase string `toml:"mysql_database" json:"mysql_database"`

	MasterProduct       string `toml:"master_product" json:"master_product"`
	MasterMysqlAddr     string `toml:"master_mysql_addr" json:"master_mysql_addr"`
	MasterMysqlUsername string `toml:"master_mysql_username" json:"master_mysql_username"`
	MasterMysqlPassword string `toml:"master_mysql_password" json:"-"`
	MasterMysqlDatabase string `toml:"master_mysql_database" json:"master_mysql_database"`
}

func NewDefaultConfig() *Config {
	c := &Config{}
	if _, err := toml.Decode(DefaultConfig, c); err != nil {
		log.PanicErrorf(err, "decode toml failed")
	}
	if err := c.Validate(); err != nil {
		log.PanicErrorf(err, "validate config failed")
	}
	return c
}

func (c *Config) LoadFromFile(path string) error {
	_, err := toml.DecodeFile(path, c)
	if err != nil {
		return errors.Trace(err)
	}
	return c.Validate()
}

func (c *Config) String() string {
	var b bytes.Buffer
	e := toml.NewEncoder(&b)
	e.Indent = "    "
	e.Encode(c)
	return b.String()
}

func (c *Config) Validate() error {
	if c.AdminAddr == "" {
		return errors.New("invalid admin_addr")
	}
	if c.ProductName == "" {
		return errors.New("invalid product_name")
	}
	if _, ok := models.ParseForwardMethod(c.MigrationMethod); !ok {
		return errors.New("invalid migration_method")
	}
	if c.MigrationParallelSlots <= 0 {
		return errors.New("invalid migration_parallel_slots")
	}
	if c.MigrationAsyncMaxBulks <= 0 {
		return errors.New("invalid migration_async_maxbulks")
	}
	if c.MigrationAsyncMaxBytes <= 0 {
		return errors.New("invalid migration_async_maxbytes")
	}
	if c.MigrationAsyncNumKeys <= 0 {
		return errors.New("invalid migration_async_numkeys")
	}
	if c.MigrationTimeout <= 0 {
		return errors.New("invalid migration_timeout")
	}
	if c.SentinelClientTimeout <= 0 {
		return errors.New("invalid sentinel_client_timeout")
	}
	if c.SentinelQuorum <= 0 {
		return errors.New("invalid sentinel_quorum")
	}
	if c.SentinelParallelSyncs <= 0 {
		return errors.New("invalid sentinel_parallel_syncs")
	}
	if c.SentinelDownAfter <= 0 {
		return errors.New("invalid sentinel_down_after")
	}
	if c.SentinelFailoverTimeout <= 0 {
		return errors.New("invalid sentinel_failover_timeout")
	}
	if c.Ncpu <= 0 {
		return errors.New("invalid ncpu")
	}
	if c.Log == "" {
		return errors.New("invalid log")
	}
	if c.ExpireLogDays < 0 {
		return errors.New("invalid expire_log_days")
	}
	if c.LogLevel == "" {
		return errors.New("invalid log_level")
	}
	if c.PidFile == "" {
		return errors.New("invalid pidfile")
	}
	return nil
}
