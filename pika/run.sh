make

mkdir -p /data01/xcache/pika/log
mkdir -p /data01/xcache/pika/binlog
mkdir -p /data01/xcache/pika/dump
mkdir -p /data01/xcache/pika/db
mkdir -p /data01/xcache/pika/dbsync
mkdir -p /data01/xcache/pika/pid

cp -f /data01/xcache/pika/conf /data01/xcache/pika/output/conf

rm -rf /data01/xcache/pika/pid/*

cd /data01/xcache/pika/output

./bin/pika -c ./conf/pika_1300.conf
./bin/pika -c ./conf/pika_1301.conf
./bin/pika -c ./conf/pika_1302.conf
./bin/pika -c ./conf/pika_1303.conf

ps -aux | grep pika
