zaqar
-

Log token search and reporting tool.

The Name
-
In Mesopotamian mythology, Zaqar or Dzakar is the messenger of the god Sin. He relays these messages to mortals through his power over their dreams and nightmares.

What it does
-

The config file lists one or more log files to watch. Each log file in the config file also lists a number of matchers that will be applied to each new log line read.

All matches are aggregated per log per run and a report is sent to the configured email address via Mailgun.

Architecture
-

Each log is processed in its own concurrent goroutine. Each log has its own concurrent set of matchers started to do concurrent matching of criteria. Any matches are synchronised to a collector that is used to send any error reports off at the end of a log run. 
