set -o errexit
set -o nounset
set -o pipefail

IMAGE_NAME=hzy46/hivedscheduler:test

pushd $(dirname "$0") > /dev/null

cd ..
sudo docker build -t hzy46/hivedscheduler:test -f ./build/hivedscheduler/Dockerfile .

popd > /dev/null

echo Succeeded to build docker image ${IMAGE_NAME}
