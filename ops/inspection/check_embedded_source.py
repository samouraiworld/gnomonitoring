from pathlib import Path
root=Path(__file__).resolve().parents[2]
workflow=(root/'.github/workflows/main.yml').read_text()
embedded=workflow.split("<<'REMOTE_BACKUP_INVENTORY'\n",1)[1].split('          REMOTE_BACKUP_INVENTORY',1)[0]
actual=''.join(line[10:]+'\n' for line in embedded.splitlines())
assert actual==(root/'ops/inspection/backup_metadata.py').read_text()
print('Embedded remote source matches the boundary-tested script.')
