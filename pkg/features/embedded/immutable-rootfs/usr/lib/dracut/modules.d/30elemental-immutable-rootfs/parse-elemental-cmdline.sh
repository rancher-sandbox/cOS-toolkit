#!/bin/bash
# immutable root is specified with
# rd.cos.mount=LABEL=<vol_label>:<mountpoint>
# rd.cos.mount=UUID=<vol_uuid>:<mountpoint>
# rd.cos.overlay=tmpfs:<size>
# rd.cos.overlay=LABEL=<vol_label>
# rd.cos.overlay=UUID=<vol_uuid>
# rd.cos.oemtimeout=<seconds>
# rd.cos.oemlabel=<vol_label>
# elemental.oemlabel=<vol_label>
# rd.cos.debugrw
# rd.cos.disable
# elemental.disable
# cos-img/filename=/cOS/active.img
# elemental.image=active

type getarg >/dev/null 2>&1 || . /lib/dracut-lib.sh

if getargbool 0 rd.cos.disable; then
    return 0
fi

if getargbool 0 elemental.disable; then
    return 0
fi

cos_img=$(getarg cos-img/filename=)
elemental_img=$(getarg elemental.image=)
[ -z "${cos_img}" && -z "${elemental_img}" ] && return 0
[ -z "${root}" ] && root=$(getarg root=)

[ -n "${elemental_img}" ] && cos_img="${elemental_img}"

cos_root_perm="ro"
if getargbool 0 rd.cos.debugrw; then
    cos_root_perm="rw"
fi

case "${root}" in
    LABEL=*) \
        root="${root//\//\\x2f}"
        root="/dev/disk/by-label/${root#LABEL=}"
        rootok=1 ;;
    UUID=*) \
        root="/dev/disk/by-uuid/${root#UUID=}"
        rootok=1 ;;
    /dev/*) \
        root="${root}"
        rootok=1 ;;
esac

[ "${rootok}" != "1" ] && return 0

info "root device set to root=${root}"
info "image set to=${cos_img}"

wait_for_dev -n "${root#block:}"

# Only run filesystem checks on force mode
fsck_mode=$(getarg fsck.mode=)
if [ "${fsck_mode}" == "force" ]; then
    /sbin/initqueue --finished --unique /sbin/elemental-fsck
fi

# set sentinel file for boot mode
mkdir -p /run/cos
case "${cos_img}" in
    *recovery*)
        echo -n 1 > /run/cos/recovery_mode ;;
    *active*)
        echo -n 1 > /run/cos/active_mode ;;
    *passive*)
        echo -n 1 > /run/cos/passive_mode ;;
esac

mkdir -p /run/elemental
case "${cos_img}" in
    *recovery*)
        echo -n 1 > /run/elemental/recovery_mode ;;
    *active*)
        echo -n 1 > /run/elemental/active_mode ;;
    *passive*)
        echo -n 1 > /run/elemental/passive_mode ;;
esac

return 0