#include <sys/types.h>
#include <stdint.h>
#include <stdarg.h>
#include <stdlib.h>
#include <string.h>
#include <stddef.h>
#include <time.h>
#include <sys/time.h>
#include <libzfs_core.h>
#include <libzfs/sys/bitops.h>

// Stuff copied from ZFS source. Unlikely to change much.
#define MAXNAMELEN 256
    struct drr_begin {
        uint64_t drr_magic;
        uint64_t drr_versioninfo; /* was drr_version */
        uint64_t drr_creation_time;
        dmu_objset_type_t drr_type;
        uint32_t drr_flags;
        uint64_t drr_toguid;
        uint64_t drr_fromguid;
        char drr_toname[MAXNAMELEN];
    };
    typedef struct zio_cksum {
        uint64_t zc_word[4];
    } zio_cksum_t;
    typedef struct dmu_replay_record {
        enum {
            DRR_BEGIN, DRR_OBJECT, DRR_FREEOBJECTS,
            DRR_WRITE, DRR_FREE, DRR_END, DRR_WRITE_BYREF,
            DRR_SPILL, DRR_WRITE_EMBEDDED, DRR_NUMTYPES
        } drr_type;
        uint32_t drr_payloadlen;
        union {
            struct drr_begin drr_begin;
            struct drr_end {
			  zio_cksum_t drr_checksum;
			  uint64_t drr_toguid;
		    } drr_end;
            /* ... */
            struct drr_checksum {
                uint64_t drr_pad[34];
                zio_cksum_t drr_checksum;
            } drr_checksum;
        } drr_u;
    } dmu_replay_record_t;
#define	DMU_BACKUP_MAGIC 0x2F5bacbacULL
#define	DMU_SET_FEATUREFLAGS(vi, x)	BF64_SET((vi), 2, 30, x)
#define	DMU_SET_STREAM_HDRTYPE(vi, x)	BF64_SET((vi), 0, 2, x)
// End stuff copied.

void computeFletcher(uint64_t chksum[4], dmu_replay_record_t* record, boolean_t set);

void computeFletcher(uint64_t chksum[4], dmu_replay_record_t* record, boolean_t set){
    const uint32_t *p = (const uint32_t*)record;
	const uint32_t *pend = p + ((sizeof(dmu_replay_record_t) - sizeof(zio_cksum_t)) / sizeof (uint32_t));
    while(p < pend){
        chksum[0] += *p;
        chksum[1] += chksum[0];
        chksum[2] += chksum[1];
        chksum[3] += chksum[2];
        p++;
    }
    if(set){
            memcpy(&record->drr_u.drr_checksum.drr_checksum, chksum, sizeof(zio_cksum_t));
    }
    p = (const uint32_t*)&record->drr_u.drr_checksum.drr_checksum;
    pend = p + (sizeof(zio_cksum_t) / sizeof (uint32_t));
    while(p < pend){
        chksum[0] += *p;
        chksum[1] += chksum[0];
        chksum[2] += chksum[1];
        chksum[3] += chksum[2];
        p++;
    }
}

int main(int argc, char**argv){
    if (argc!=5){
        printf("Usage: %s creation_time from_guid to_guid snapshot_name\n", argv[0]);
        return 1;
    }
    dmu_replay_record_t r_begin, r_end;
    r_begin.drr_payloadlen = 0;
    r_begin.drr_type = DRR_BEGIN;
    r_begin.drr_u.drr_begin.drr_magic = DMU_BACKUP_MAGIC;
    r_begin.drr_u.drr_begin.drr_creation_time = strtoull(argv[1], NULL, 10);
    strncpy(r_begin.drr_u.drr_begin.drr_toname, "w@", MAXNAMELEN);
    strncat(r_begin.drr_u.drr_begin.drr_toname, argv[4], MAXNAMELEN-3);
    r_begin.drr_u.drr_begin.drr_fromguid = strtoull(argv[2], NULL, 10);
    r_begin.drr_u.drr_begin.drr_toguid = strtoull(argv[3], NULL, 10);
    r_begin.drr_u.drr_begin.drr_flags = 0xc;
    r_begin.drr_u.drr_begin.drr_type = (dmu_objset_type_t)2;
    DMU_SET_STREAM_HDRTYPE(r_begin.drr_u.drr_begin.drr_versioninfo, 1);
    DMU_SET_FEATUREFLAGS(r_begin.drr_u.drr_begin.drr_versioninfo, 4);
   
    r_end.drr_payloadlen = 0;
    r_end.drr_type = DRR_END;
    r_end.drr_u.drr_end.drr_toguid =     r_begin.drr_u.drr_begin.drr_toguid;
    uint64_t chksum[4] = {0,0,0,0}; 
    computeFletcher(chksum, &r_begin,B_FALSE);
    memcpy(&r_end.drr_u.drr_end.drr_checksum, chksum, sizeof(zio_cksum_t));
    computeFletcher(chksum, &r_end,B_TRUE);
    write(1,&r_begin, sizeof(r_begin));
    write(1,&r_end, sizeof(r_end));
    return 0;
}



