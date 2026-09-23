#!/bin/bash

for i in $(seq 1 30); do
    curl -s -o /dev/null -w "%{http_code}\n" -m 2 -H 'Authorization: Bearer token' http://192.168.5.242:30222/ping
done | sort | uniq -c