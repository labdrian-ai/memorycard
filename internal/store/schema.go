package store

// SchemaVersion is the number of entries in migrations.
const SchemaVersion = 2

// migrations[i] upgrades a database from version i to i+1.
var migrations = [...]string{
	// v1: episodic core.
	`
create table memories (
	id            integer primary key autoincrement,
	project       text not null,
	scope         text not null default 'project' check (scope in ('project','personal','global')),
	type          text not null default 'manual',
	title         text not null,
	body          text not null,
	topic_key     text,
	content_hash  text not null,
	pinned        integer not null default 0,
	status        text not null default 'active' check (status in ('active','superseded','archived')),
	superseded_by integer references memories(id),
	use_count     integer not null default 0,
	last_used_at  text,
	created_at    text not null,
	updated_at    text not null
);
create index idx_memories_hash  on memories(project, content_hash);
create index idx_memories_topic on memories(project, scope, topic_key);
create index idx_memories_status on memories(status);

create table memory_versions (
	memory_id integer not null references memories(id) on delete cascade,
	version   integer not null,
	title     text not null,
	body      text not null,
	saved_at  text not null,
	primary key (memory_id, version)
);

create table vectors (
	memory_id integer primary key references memories(id) on delete cascade,
	model     text not null,
	dim       integer not null,
	vec       blob not null
);

create table sessions (
	id         text primary key,
	project    text not null,
	started_at text not null,
	ended_at   text
);

create virtual table fts using fts5(
	title, body,
	content = 'memories', content_rowid = 'id',
	tokenize = 'unicode61 remove_diacritics 2'
);

create trigger memories_ai after insert on memories begin
	insert into fts(rowid, title, body) values (new.id, new.title, new.body);
end;
create trigger memories_ad after delete on memories begin
	insert into fts(fts, rowid, title, body) values ('delete', old.id, old.title, old.body);
end;
create trigger memories_au after update of title, body on memories begin
	insert into fts(fts, rowid, title, body) values ('delete', old.id, old.title, old.body);
	insert into fts(rowid, title, body) values (new.id, new.title, new.body);
end;
`,
	// v2: remembers which source record became which memory, so an import can
	// resume and so supersession links can be resolved by source key.
	`
create table import_map (
	source     text    not null,
	source_key text    not null,
	part       integer not null default 1,
	memory_id  integer not null references memories(id) on delete cascade,
	primary key (source, source_key, part)
);
create index idx_import_map_memory on import_map(memory_id);
`,
}
