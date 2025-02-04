# Summary for single suite

## Aggregator Results

| Test Name | readIOPS | writeIOPS | readBW (MB/s) | writeBW (MB/s) | readLat (ms) | writeLat (ms) | CPUUsage |
|-----------|----------|-----------|---------------|----------------|--------------|---------------|----------|
| "r-bw-single-test" | "3648.55" ("3648.55"–"3648.55") | "0.00" ("0.00"–"0.00") | "3656.93" ("3656.93"–"3656.93") | "0.00" ("0.00"–"0.00") | "140.21" ("140.21"–"140.21") | "0.00" ("0.00"–"0.00") | "2.79" ("2.79"–"2.79") |
| "r-lat-single-test" | "4505.37" ("4505.37"–"4505.37") | "0.00" ("0.00"–"0.00") | "17.60" ("17.60"–"17.60") | "0.00" ("0.00"–"0.00") | "0.22" ("0.22"–"0.22") | "0.00" ("0.00"–"0.00") | "7.52" ("7.52"–"7.52") |
| "rw-generic-single-test" | "28394.08" ("28394.08"–"28394.08") | "12201.27" ("12201.27"–"12201.27") | "1774.68" ("1774.68"–"1774.68") | "762.59" ("762.59"–"762.59") | "2.11" ("2.11"–"2.11") | "0.33" ("0.33"–"0.33") | "16.08" ("16.08"–"16.08") |
| "w-bw-single-test" | "0.00" ("0.00"–"0.00") | "3802.14" ("3802.14"–"3802.14") | "0.00" ("0.00"–"0.00") | "3810.52" ("3810.52"–"3810.52") | "0.00" ("0.00"–"0.00") | "134.59" ("134.59"–"134.59") | "5.57" ("5.57"–"5.57") |
| "w-lat-single-test" | "0.00" ("0.00"–"0.00") | "18872.04" ("18872.04"–"18872.04") | "0.00" ("0.00"–"0.00") | "73.72" ("73.72"–"73.72") | "0.00" ("0.00"–"0.00") | "0.05" ("0.05"–"0.05") | "24.66" ("24.66"–"24.66") |
| "wr-generic-single-test" | "11465.44" ("11465.44"–"11465.44") | "26763.48" ("26763.48"–"26763.48") | "716.61" ("716.61"–"716.61") | "1672.76" ("1672.76"–"1672.76") | "2.81" ("2.81"–"2.81") | "1.19" ("1.19"–"1.19") | "13.25" ("13.25"–"13.25") |

## Parameters

| Test Name | tool | parameters |
|-----------|------|------------|
| "r-bw-single-test" | "fio" | "60s" "bs=1m, buffered=0, direct=1, invalidate=1, iodepth=64, ioengine=libaio, numjobs=8, ramp_time=5, rw=randread, size=9Gi" |
| "r-lat-single-test" | "fio" | "60s" "bs=4k, buffered=0, direct=1, invalidate=1, iodepth=1, ioengine=libaio, numjobs=1, ramp_time=5, rw=read, size=1Gi" |
| "rw-generic-single-test" | "fio" | "60s" "bs=64k, buffered=0, direct=1, invalidate=1, iodepth=16, ioengine=libaio, numjobs=4, ramp_time=5, rw=rw, rwmixread=70, size=9Gi" |
| "w-bw-single-test" | "fio" | "60s" "bs=1m, buffered=0, direct=1, invalidate=1, iodepth=64, ioengine=libaio, numjobs=8, ramp_time=5, rw=randwrite, size=9Gi" |
| "w-lat-single-test" | "fio" | "60s" "bs=4k, buffered=0, direct=1, invalidate=1, iodepth=1, ioengine=libaio, numjobs=1, ramp_time=5, rw=write, size=1Gi" |
| "wr-generic-single-test" | "fio" | "60s" "bs=64k, buffered=0, direct=1, invalidate=1, iodepth=16, ioengine=libaio, numjobs=4, ramp_time=5, rw=rw, rwmixread=30, size=9Gi" |

