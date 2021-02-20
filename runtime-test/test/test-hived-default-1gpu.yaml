apiVersion: v1
kind: Pod
metadata:
  annotations:
    hivedscheduler.microsoft.com/pod-scheduling-spec: |
      virtualCluster: default
      priority: 10
      pinnedCellId: null
      leafCellType: GENERIC-WORKER
      leafCellNumber: 1
      affinityGroup:
        name: test-hived-default-1gpu
        members:
          - podNumber: 1
            leafCellNumber: 1
  name: test-hived-default-1gpu
  namespace: default
spec:
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: pai-worker
            operator: In
            values:
            - "true"
  containers:
  - command:
    - "bash"
    - "-c"
    - "nvidia-smi && sleep 1000d"
    env:
    - name: NVIDIA_VISIBLE_DEVICES
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['hivedscheduler.microsoft.com/pod-leaf-cell-isolation']
    - name: HIVED_VISIBLE_DEVICES
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['hivedscheduler.microsoft.com/pod-leaf-cell-isolation']
    image: openpai/standard:python_3.6-pytorch_1.2.0-gpu
    imagePullPolicy: Always
    name: app
    resources:
      limits:
        cpu: "3"
        github.com/fuse: "1"
        hivedscheduler.microsoft.com/pod-scheduling-enable: "1"
        memory: 29065Mi
      requests:
        cpu: "3"
        github.com/fuse: "1"
        hivedscheduler.microsoft.com/pod-scheduling-enable: "1"
        memory: 29065Mi
  hostNetwork: true
  schedulerName: test-hivedscheduler-ds-default
