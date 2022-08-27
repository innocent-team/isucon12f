# This file is auto-generated by the Perl DateTime Suite time zone
# code generator (0.08) This code generator comes with the
# DateTime::TimeZone module distribution in the tools/ directory

#
# Generated from /tmp/QmbiVitAXO/africa.  Olson data version 2022b
#
# Do not edit this file directly.
#
package DateTime::TimeZone::Africa::Sao_Tome;

use strict;
use warnings;
use namespace::autoclean;

our $VERSION = '2.53';

use Class::Singleton 1.03;
use DateTime::TimeZone;
use DateTime::TimeZone::OlsonDB;

@DateTime::TimeZone::Africa::Sao_Tome::ISA = ( 'Class::Singleton', 'DateTime::TimeZone' );

my $spans =
[
    [
DateTime::TimeZone::NEG_INFINITY, #    utc_start
59421771184, #      utc_end 1883-12-31 23:33:04 (Mon)
DateTime::TimeZone::NEG_INFINITY, #  local_start
59421772800, #    local_end 1884-01-01 00:00:00 (Tue)
1616,
0,
'LMT',
    ],
    [
59421771184, #    utc_start 1883-12-31 23:33:04 (Mon)
60305299200, #      utc_end 1912-01-01 00:00:00 (Mon)
59421768979, #  local_start 1883-12-31 22:56:19 (Mon)
60305296995, #    local_end 1911-12-31 23:23:15 (Sun)
-2205,
0,
'LMT',
    ],
    [
60305299200, #    utc_start 1912-01-01 00:00:00 (Mon)
63650451600, #      utc_end 2018-01-01 01:00:00 (Mon)
60305299200, #  local_start 1912-01-01 00:00:00 (Mon)
63650451600, #    local_end 2018-01-01 01:00:00 (Mon)
0,
0,
'GMT',
    ],
    [
63650451600, #    utc_start 2018-01-01 01:00:00 (Mon)
63681987600, #      utc_end 2019-01-01 01:00:00 (Tue)
63650455200, #  local_start 2018-01-01 02:00:00 (Mon)
63681991200, #    local_end 2019-01-01 02:00:00 (Tue)
3600,
0,
'WAT',
    ],
    [
63681987600, #    utc_start 2019-01-01 01:00:00 (Tue)
DateTime::TimeZone::INFINITY, #      utc_end
63681987600, #  local_start 2019-01-01 01:00:00 (Tue)
DateTime::TimeZone::INFINITY, #    local_end
0,
0,
'GMT',
    ],
];

sub olson_version {'2022b'}

sub has_dst_changes {0}

sub _max_year {2032}

sub _new_instance {
    return shift->_init( @_, spans => $spans );
}



1;

