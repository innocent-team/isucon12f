# This file is auto-generated by the Perl DateTime Suite time zone
# code generator (0.08) This code generator comes with the
# DateTime::TimeZone module distribution in the tools/ directory

#
# Generated from /tmp/QmbiVitAXO/australasia.  Olson data version 2022b
#
# Do not edit this file directly.
#
package DateTime::TimeZone::Pacific::Nauru;

use strict;
use warnings;
use namespace::autoclean;

our $VERSION = '2.53';

use Class::Singleton 1.03;
use DateTime::TimeZone;
use DateTime::TimeZone::OlsonDB;

@DateTime::TimeZone::Pacific::Nauru::ISA = ( 'Class::Singleton', 'DateTime::TimeZone' );

my $spans =
[
    [
DateTime::TimeZone::NEG_INFINITY, #    utc_start
60590551940, #      utc_end 1921-01-14 12:52:20 (Fri)
DateTime::TimeZone::NEG_INFINITY, #  local_start
60590592000, #    local_end 1921-01-15 00:00:00 (Sat)
40060,
0,
'LMT',
    ],
    [
60590551940, #    utc_start 1921-01-14 12:52:20 (Fri)
61272765000, #      utc_end 1942-08-28 12:30:00 (Fri)
60590593340, #  local_start 1921-01-15 00:22:20 (Sat)
61272806400, #    local_end 1942-08-29 00:00:00 (Sat)
41400,
0,
'+1130',
    ],
    [
61272765000, #    utc_start 1942-08-28 12:30:00 (Fri)
61368332400, #      utc_end 1945-09-07 15:00:00 (Fri)
61272797400, #  local_start 1942-08-28 21:30:00 (Fri)
61368364800, #    local_end 1945-09-08 00:00:00 (Sat)
32400,
0,
'+09',
    ],
    [
61368332400, #    utc_start 1945-09-07 15:00:00 (Fri)
62423101800, #      utc_end 1979-02-09 14:30:00 (Fri)
61368373800, #  local_start 1945-09-08 02:30:00 (Sat)
62423143200, #    local_end 1979-02-10 02:00:00 (Sat)
41400,
0,
'+1130',
    ],
    [
62423101800, #    utc_start 1979-02-09 14:30:00 (Fri)
DateTime::TimeZone::INFINITY, #      utc_end
62423145000, #  local_start 1979-02-10 02:30:00 (Sat)
DateTime::TimeZone::INFINITY, #    local_end
43200,
0,
'+12',
    ],
];

sub olson_version {'2022b'}

sub has_dst_changes {0}

sub _max_year {2032}

sub _new_instance {
    return shift->_init( @_, spans => $spans );
}



1;

