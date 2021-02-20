# 删除多个hived pod
# 需要满足：文件名是<pod-name>.yaml

FILEPATH=$1
FILENAME=`basename $FILEPATH`
PODNAME=`echo $FILENAME | cut -d '.' -f 1`

kubectl get po | grep ^${PODNAME}-.\\{5\\} | awk '{print $1}' | while read po; do kubectl delete po $po &; done
wait