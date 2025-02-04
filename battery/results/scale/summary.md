# Summary for scale suite

## Aggregator Results

| Test Name | readIOPS | writeIOPS | readBW (MB/s) | writeBW (MB/s) | readLat (ms) | writeLat (ms) | CPUUsage |
|-----------|----------|-----------|---------------|----------------|--------------|---------------|----------|
| "r-bw-scale-test" | "680.07" ("407.28"–"1315.01") | "0.00" ("0.00"–"0.00") | "688.23" ("415.54"–"1323.28") | "0.00" ("0.00"–"0.00") | "828.51" ("389.68"–"1254.96") | "0.00" ("0.00"–"0.00") | "0.56" ("0.31"–"1.14") |
| "r-lat-scale-test" | "3925.59" ("3822.24"–"4110.20") | "0.00" ("0.00"–"0.00") | "15.33" ("14.93"–"16.05") | "0.00" ("0.00"–"0.00") | "0.25" ("0.24"–"0.26") | "0.00" ("0.00"–"0.00") | "7.56" ("7.24"–"7.86") |
| "rw-generic-scale-test" | "9224.44" ("7785.74"–"10575.32") | "3960.14" ("3345.15"–"4541.87") | "576.58" ("486.67"–"661.02") | "247.52" ("209.07"–"283.87") | "6.16" ("5.48"–"7.33") | "1.80" ("1.02"–"3.41") | "8.10" ("6.62"–"9.69") |
| "w-bw-scale-test" | "0.00" ("0.00"–"0.00") | "279.94" ("181.73"–"338.03") | "0.00" ("0.00"–"0.00") | "288.18" ("190.02"–"346.38") | "0.00" ("0.00"–"0.00") | "1840.28" ("1560.06"–"2776.89") | "0.42" ("0.30"–"0.50") |
| "w-lat-scale-test" | "0.00" ("0.00"–"0.00") | "17320.25" ("16153.38"–"18621.71") | "0.00" ("0.00"–"0.00") | "67.66" ("63.10"–"72.74") | "0.00" ("0.00"–"0.00") | "0.06" ("0.05"–"0.06") | "24.10" ("23.00"–"25.05") |
| "wr-generic-scale-test" | "1966.28" ("1749.38"–"2864.61") | "4582.62" ("4075.16"–"6676.08") | "122.90" ("109.35"–"179.06") | "286.47" ("254.74"–"417.30") | "3.28" ("2.86"–"4.06") | "12.71" ("8.27"–"14.13") | "3.23" ("2.75"–"4.20") |

## Parameters

| Test Name | tool | parameters |
|-----------|------|------------|
| "r-bw-scale-test" | "fio" | "60s" "bs=1m, buffered=0, direct=1, invalidate=1, iodepth=64, ioengine=libaio, numjobs=8, ramp_time=5, rw=randread, size=9Gi" |
| "r-lat-scale-test" | "fio" | "60s" "bs=4k, buffered=0, direct=1, invalidate=1, iodepth=1, ioengine=libaio, numjobs=1, ramp_time=5, rw=read, size=1Gi" |
| "rw-generic-scale-test" | "fio" | "60s" "bs=64k, buffered=0, direct=1, invalidate=1, iodepth=16, ioengine=libaio, numjobs=4, ramp_time=5, rw=rw, rwmixread=70, size=9Gi" |
| "w-bw-scale-test" | "fio" | "60s" "bs=1m, buffered=0, direct=1, invalidate=1, iodepth=64, ioengine=libaio, numjobs=8, ramp_time=5, rw=randwrite, size=9Gi" |
| "w-lat-scale-test" | "fio" | "60s" "bs=4k, buffered=0, direct=1, invalidate=1, iodepth=1, ioengine=libaio, numjobs=1, ramp_time=5, rw=write, size=1Gi" |
| "wr-generic-scale-test" | "fio" | "60s" "bs=64k, buffered=0, direct=1, invalidate=1, iodepth=16, ioengine=libaio, numjobs=4, ramp_time=5, rw=rw, rwmixread=30, size=9Gi" |

