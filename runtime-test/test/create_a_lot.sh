# 创建多个hived pod
# 需要满足：文件名是<pod-name>.yaml，且affinity group也是<pod-name>
# 使用方法 bash create_a_lot.sh <pod-name>.yaml <number>
set -o errexit
set -o nounset
set -o pipefail


FILEPATH=$1
NUM=$2
FILENAME=`basename $FILEPATH`
PODNAME=`echo $FILENAME | cut -d '.' -f 1`

for i in {1..${NUM}}
do
    RANDSTR=$(cat /dev/urandom | tr -dc 'a-z' | fold -w 5 | head -n 1)
    RANDNAME=${PODNAME}-${RANDSTR}
    cat ${FILEPATH} | sed "s/${PODNAME}/${RANDNAME}/" | kubectl apply -f -
done  