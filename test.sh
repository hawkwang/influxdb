#!/usr/bin/env bash

set -e

cd `dirname $0`
. exports.sh

function print_usage {
    echo "$0 [-o] [-p]"
    echo "  -o|--only:     Run the test that matches the given regex"
    echo "  -p|--packages: Run the test in the given packages only"
    echo "  -h|--help:     Prints this help message"
}

TEMP=`getopt -o hp:o: --long help,only:,packages: \
     -n $0 -- "$@"`

if [ $? != 0 ] ; then print_usage ; exit 1 ; fi

# Note the quotes around `$TEMP': they are essential!
eval set -- "$TEMP"

while true ; do
    case "$1" in
        -h|--help) print_usage; exit 1; shift;;
        -o|--only) regex=$2; shift 2;;
        -p|--packages) test_packages="$test_packages $2"; shift 2;;
        --) shift ; break ;;
        *) echo "Internal error!" ; exit 1 ;;
    esac
done

pushd src/parser
./build_parser.sh
if [ "x`uname`" == "xLinux" ]; then
    if ! ./test_memory_leaks.sh; then
        echo "ERROR: memory leak detected"
        exit 1
    fi
fi
popd

go get launchpad.net/gocheck

go fmt $packages

./build.sh

[ "x$test_packages" == "x" ] && test_packages="$packages"
echo "Running tests for packages: $test_packages"

[ "x$regex" != "x" ] && gocheck_args="-gocheck.f $regex"

go test $test_packages -v -gocheck.v $gocheck_args