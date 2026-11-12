export namespace bulk {

	export class RowPreview {
	    rowNo: number;
	    action: string;
	    actionLabel: string;
	    issueKey: string;
	    summary: string;
	    changes: string[];
	    conflictWarning: boolean;

	    static createFrom(source: any = {}) {
	        return new RowPreview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rowNo = source["rowNo"];
	        this.action = source["action"];
	        this.actionLabel = source["actionLabel"];
	        this.issueKey = source["issueKey"];
	        this.summary = source["summary"];
	        this.changes = source["changes"];
	        this.conflictWarning = source["conflictWarning"];
	    }
	}
	export class RowError {
	    rowNo: number;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new RowError(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rowNo = source["rowNo"];
	        this.message = source["message"];
	    }
	}
	export class ImportResult {
	    jobId: number;
	    projectId: number;
	    totalRows: number;
	    creates: number;
	    updates: number;
	    skips: number;
	    valid: boolean;
	    errors: RowError[];
	    previews: RowPreview[];
	    warnings: string[];

	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobId = source["jobId"];
	        this.projectId = source["projectId"];
	        this.totalRows = source["totalRows"];
	        this.creates = source["creates"];
	        this.updates = source["updates"];
	        this.skips = source["skips"];
	        this.valid = source["valid"];
	        this.errors = this.convertValues(source["errors"], RowError);
	        this.previews = this.convertValues(source["previews"], RowPreview);
	        this.warnings = source["warnings"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NamedID {
	    id: number;
	    name: string;

	    static createFrom(source: any = {}) {
	        return new NamedID(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}


	export class RunResult {
	    jobId: number;
	    done: number;
	    failed: number;
	    conflict: number;
	    skipped: number;
	    durationMs: number;
	    warnings: string[];

	    static createFrom(source: any = {}) {
	        return new RunResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobId = source["jobId"];
	        this.done = source["done"];
	        this.failed = source["failed"];
	        this.conflict = source["conflict"];
	        this.skipped = source["skipped"];
	        this.durationMs = source["durationMs"];
	        this.warnings = source["warnings"];
	    }
	}

}

export namespace config {

	export class Profile {
	    id: string;
	    name: string;
	    spaceUrl: string;
	    lastUserName: string;
	    lastUserId: number;
	    keyFingerprint: string;

	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.spaceUrl = source["spaceUrl"];
	        this.lastUserName = source["lastUserName"];
	        this.lastUserId = source["lastUserId"];
	        this.keyFingerprint = source["keyFingerprint"];
	    }
	}

}

export namespace customfield {

	export class Filter {
	    defId: number;
	    typeId: number;
	    text: string;
	    min: string;
	    max: string;
	    itemIds: number[];

	    static createFrom(source: any = {}) {
	        return new Filter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.defId = source["defId"];
	        this.typeId = source["typeId"];
	        this.text = source["text"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.itemIds = source["itemIds"];
	    }
	}

}

export namespace export {

	export class ColumnMeta {
	    key: string;
	    label: string;
	    byDefault: boolean;

	    static createFrom(source: any = {}) {
	        return new ColumnMeta(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.label = source["label"];
	        this.byDefault = source["byDefault"];
	    }
	}

}

export namespace main {

	export class AppVersionInfo {
	    version: string;

	    static createFrom(source: any = {}) {
	        return new AppVersionInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	    }
	}
	export class BulkJobRowDTO {
	    rowNo: number;
	    issueKey: string;
	    status: string;
	    statusLabel: string;
	    resultIssueId: number;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new BulkJobRowDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rowNo = source["rowNo"];
	        this.issueKey = source["issueKey"];
	        this.status = source["status"];
	        this.statusLabel = source["statusLabel"];
	        this.resultIssueId = source["resultIssueId"];
	        this.error = source["error"];
	    }
	}
	export class ConnectionTestResult {
	    ok: boolean;
	    userId: number;
	    userName: string;
	    roleType: number;
	    adminAvailable: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new ConnectionTestResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.userId = source["userId"];
	        this.userName = source["userName"];
	        this.roleType = source["roleType"];
	        this.adminAvailable = source["adminAvailable"];
	        this.message = source["message"];
	    }
	}
	export class CustomFieldItemDTO {
	    id: number;
	    name: string;

	    static createFrom(source: any = {}) {
	        return new CustomFieldItemDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}
	export class CustomFieldDefDTO {
	    id: number;
	    typeId: number;
	    typeName: string;
	    name: string;
	    description: string;
	    required: boolean;
	    applicableIssueTypes: number[];
	    allowInput: boolean;
	    allowAddItem: boolean;
	    items: CustomFieldItemDTO[];

	    static createFrom(source: any = {}) {
	        return new CustomFieldDefDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.typeId = source["typeId"];
	        this.typeName = source["typeName"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.required = source["required"];
	        this.applicableIssueTypes = source["applicableIssueTypes"];
	        this.allowInput = source["allowInput"];
	        this.allowAddItem = source["allowAddItem"];
	        this.items = this.convertValues(source["items"], CustomFieldItemDTO);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class ExportResultDTO {
	    path: string;
	    rows: number;
	    unverifiable: number;

	    static createFrom(source: any = {}) {
	        return new ExportResultDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.rows = source["rows"];
	        this.unverifiable = source["unverifiable"];
	    }
	}
	export class FilterOptionsDTO {
	    statuses: string[];
	    assignees: string[];

	    static createFrom(source: any = {}) {
	        return new FilterOptionsDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.statuses = source["statuses"];
	        this.assignees = source["assignees"];
	    }
	}
	export class IssueCommentDTO {
	    authorName: string;
	    content: string;
	    created: string;

	    static createFrom(source: any = {}) {
	        return new IssueCommentDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.authorName = source["authorName"];
	        this.content = source["content"];
	        this.created = source["created"];
	    }
	}
	export class IssueCustomFieldDTO {
	    name: string;
	    value: string;

	    static createFrom(source: any = {}) {
	        return new IssueCustomFieldDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.value = source["value"];
	    }
	}
	export class IssueDetailDTO {
	    issueKey: string;
	    summary: string;
	    description: string;
	    statusName: string;
	    assigneeName: string;
	    issueTypeName: string;
	    priorityName: string;
	    created: string;
	    updated: string;
	    dueDate: string;
	    parentIssueKey: string;
	    customFields: IssueCustomFieldDTO[];
	    fetchedAt: string;
	    comments: IssueCommentDTO[];
	    commentsFetchedAt: string;
	    commentsHistoryOnly: number;
	    commentsTruncated: boolean;
	    warnings: string[];
	    textFormattingRule: string;

	    static createFrom(source: any = {}) {
	        return new IssueDetailDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issueKey = source["issueKey"];
	        this.summary = source["summary"];
	        this.description = source["description"];
	        this.statusName = source["statusName"];
	        this.assigneeName = source["assigneeName"];
	        this.issueTypeName = source["issueTypeName"];
	        this.priorityName = source["priorityName"];
	        this.created = source["created"];
	        this.updated = source["updated"];
	        this.dueDate = source["dueDate"];
	        this.parentIssueKey = source["parentIssueKey"];
	        this.customFields = this.convertValues(source["customFields"], IssueCustomFieldDTO);
	        this.fetchedAt = source["fetchedAt"];
	        this.comments = this.convertValues(source["comments"], IssueCommentDTO);
	        this.commentsFetchedAt = source["commentsFetchedAt"];
	        this.commentsHistoryOnly = source["commentsHistoryOnly"];
	        this.commentsTruncated = source["commentsTruncated"];
	        this.warnings = source["warnings"];
	        this.textFormattingRule = source["textFormattingRule"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class IssueRowDTO {
	    issueKey: string;
	    summary: string;
	    statusName: string;
	    assigneeName: string;
	    issueTypeName: string;
	    priorityName: string;
	    created: string;
	    updated: string;
	    dueDate: string;
	    customFields: Record<string, string>;

	    static createFrom(source: any = {}) {
	        return new IssueRowDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issueKey = source["issueKey"];
	        this.summary = source["summary"];
	        this.statusName = source["statusName"];
	        this.assigneeName = source["assigneeName"];
	        this.issueTypeName = source["issueTypeName"];
	        this.priorityName = source["priorityName"];
	        this.created = source["created"];
	        this.updated = source["updated"];
	        this.dueDate = source["dueDate"];
	        this.customFields = source["customFields"];
	    }
	}
	export class IssueSearchDTO {
	    rows: IssueRowDTO[];
	    total: number;
	    unverifiable: number;

	    static createFrom(source: any = {}) {
	        return new IssueSearchDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], IssueRowDTO);
	        this.total = source["total"];
	        this.unverifiable = source["unverifiable"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogInfo {
	    path: string;
	    enabled: boolean;

	    static createFrom(source: any = {}) {
	        return new LogInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.enabled = source["enabled"];
	    }
	}
	export class MasterDataDTO {
	    issueTypes: bulk.NamedID[];
	    priorities: bulk.NamedID[];
	    statuses: bulk.NamedID[];
	    customFields: CustomFieldDefDTO[];

	    static createFrom(source: any = {}) {
	        return new MasterDataDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issueTypes = this.convertValues(source["issueTypes"], bulk.NamedID);
	        this.priorities = this.convertValues(source["priorities"], bulk.NamedID);
	        this.statuses = this.convertValues(source["statuses"], bulk.NamedID);
	        this.customFields = this.convertValues(source["customFields"], CustomFieldDefDTO);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProfileInput {
	    id: string;
	    name: string;
	    spaceUrl: string;
	    apiKey: string;

	    static createFrom(source: any = {}) {
	        return new ProfileInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.spaceUrl = source["spaceUrl"];
	        this.apiKey = source["apiKey"];
	    }
	}
	export class ProjectRow {
	    id: number;
	    projectKey: string;
	    name: string;
	    lastSyncedAt: string;
	    syncStateUnknown: boolean;

	    static createFrom(source: any = {}) {
	        return new ProjectRow(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.projectKey = source["projectKey"];
	        this.name = source["name"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	        this.syncStateUnknown = source["syncStateUnknown"];
	    }
	}
	export class StorageInfo {
	    configDir: string;
	    storageMode: string;
	    databases: service.DatabaseInfo[];
	    logPath: string;
	    logEnabled: boolean;

	    static createFrom(source: any = {}) {
	        return new StorageInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configDir = source["configDir"];
	        this.storageMode = source["storageMode"];
	        this.databases = this.convertValues(source["databases"], service.DatabaseInfo);
	        this.logPath = source["logPath"];
	        this.logEnabled = source["logEnabled"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SyncResultDTO {
	    mode: string;
	    fetched: number;
	    upserted: number;
	    deleted: number;
	    warnings: string[];
	    durationMs: number;

	    static createFrom(source: any = {}) {
	        return new SyncResultDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.fetched = source["fetched"];
	        this.upserted = source["upserted"];
	        this.deleted = source["deleted"];
	        this.warnings = source["warnings"];
	        this.durationMs = source["durationMs"];
	    }
	}
	export class SyncStateRow {
	    dataKind: string;
	    projectId: number;
	    lastSyncedAt: string;

	    static createFrom(source: any = {}) {
	        return new SyncStateRow(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dataKind = source["dataKind"];
	        this.projectId = source["projectId"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	    }
	}
	export class UserListDTO {
	    rows: store.UserRow[];
	    total: number;

	    static createFrom(source: any = {}) {
	        return new UserListDTO(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rows = this.convertValues(source["rows"], store.UserRow);
	        this.total = source["total"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace service {

	export class DatabaseInfo {
	    profileId: string;
	    profileName: string;
	    path: string;
	    sizeBytes: number;
	    exists: boolean;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new DatabaseInfo(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profileId = source["profileId"];
	        this.profileName = source["profileName"];
	        this.path = source["path"];
	        this.sizeBytes = source["sizeBytes"];
	        this.exists = source["exists"];
	        this.error = source["error"];
	    }
	}
	export class PermissionStatus {
	    adminAvailable: boolean;
	    degraded: boolean;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new PermissionStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.adminAvailable = source["adminAvailable"];
	        this.degraded = source["degraded"];
	        this.message = source["message"];
	    }
	}
	export class RateLimitCategory {
	    name: string;
	    limit: number;
	    remaining: number;
	    resetUnix: number;
	    observed: boolean;

	    static createFrom(source: any = {}) {
	        return new RateLimitCategory(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.limit = source["limit"];
	        this.remaining = source["remaining"];
	        this.resetUnix = source["resetUnix"];
	        this.observed = source["observed"];
	    }
	}
	export class RateLimitStatus {
	    categories: RateLimitCategory[];

	    static createFrom(source: any = {}) {
	        return new RateLimitStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.categories = this.convertValues(source["categories"], RateLimitCategory);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WikiCreateInput {
	    projectId: number;
	    name: string;
	    content: string;
	    mailNotify: boolean;

	    static createFrom(source: any = {}) {
	        return new WikiCreateInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.projectId = source["projectId"];
	        this.name = source["name"];
	        this.content = source["content"];
	        this.mailNotify = source["mailNotify"];
	    }
	}
	export class WikiPage {
	    id: number;
	    projectId: number;
	    name: string;
	    content: string;
	    tagsJson: string;
	    created: string;
	    updated: string;
	    contentUpdated: string;
	    rawJson: string;
	    listFetchedAt: string;
	    contentFetchedAt: string;
	    deleted: boolean;
	    tags: string[];
	    createdUser: string;
	    updatedUser: string;
	    textFormattingRule: string;
	    attachments: store.WikiAttachment[];

	    static createFrom(source: any = {}) {
	        return new WikiPage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.projectId = source["projectId"];
	        this.name = source["name"];
	        this.content = source["content"];
	        this.tagsJson = source["tagsJson"];
	        this.created = source["created"];
	        this.updated = source["updated"];
	        this.contentUpdated = source["contentUpdated"];
	        this.rawJson = source["rawJson"];
	        this.listFetchedAt = source["listFetchedAt"];
	        this.contentFetchedAt = source["contentFetchedAt"];
	        this.deleted = source["deleted"];
	        this.tags = source["tags"];
	        this.createdUser = source["createdUser"];
	        this.updatedUser = source["updatedUser"];
	        this.textFormattingRule = source["textFormattingRule"];
	        this.attachments = this.convertValues(source["attachments"], store.WikiAttachment);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WikiEdit {
	    token: string;
	    baselineUpdated: string;
	    page?: WikiPage;

	    static createFrom(source: any = {}) {
	        return new WikiEdit(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = source["token"];
	        this.baselineUpdated = source["baselineUpdated"];
	        this.page = this.convertValues(source["page"], WikiPage);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WikiExportEstimate {
	    pages: number;
	    attachments: number;
	    bytes: number;
	    estimatedSeconds: number;
	    missingContent: number;
	    lastSyncedAt: string;
	    warnings: string[];

	    static createFrom(source: any = {}) {
	        return new WikiExportEstimate(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pages = source["pages"];
	        this.attachments = source["attachments"];
	        this.bytes = source["bytes"];
	        this.estimatedSeconds = source["estimatedSeconds"];
	        this.missingContent = source["missingContent"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	        this.warnings = source["warnings"];
	    }
	}
	export class WikiImageInput {
	    projectId: number;
	    wikiId: number;
	    attachmentId: number;
	    expectedUpdated: string;
	    requestId: string;

	    static createFrom(source: any = {}) {
	        return new WikiImageInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.projectId = source["projectId"];
	        this.wikiId = source["wikiId"];
	        this.attachmentId = source["attachmentId"];
	        this.expectedUpdated = source["expectedUpdated"];
	        this.requestId = source["requestId"];
	    }
	}
	export class WikiImageResult {
	    code: string;
	    mime: string;
	    dataBase64: string;
	    width: number;
	    height: number;

	    static createFrom(source: any = {}) {
	        return new WikiImageResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.mime = source["mime"];
	        this.dataBase64 = source["dataBase64"];
	        this.width = source["width"];
	        this.height = source["height"];
	    }
	}
	export class WikiList {
	    pages: WikiPage[];
	    disabled: boolean;
	    lastSyncedAt: string;
	    textFormattingRule: string;

	    static createFrom(source: any = {}) {
	        return new WikiList(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pages = this.convertValues(source["pages"], WikiPage);
	        this.disabled = source["disabled"];
	        this.lastSyncedAt = source["lastSyncedAt"];
	        this.textFormattingRule = source["textFormattingRule"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class WikiSaveInput {
	    token: string;
	    name: string;
	    content: string;
	    mailNotify: boolean;
	    overwrite: boolean;

	    static createFrom(source: any = {}) {
	        return new WikiSaveInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = source["token"];
	        this.name = source["name"];
	        this.content = source["content"];
	        this.mailNotify = source["mailNotify"];
	        this.overwrite = source["overwrite"];
	    }
	}
	export class WikiWriteResult {
	    status: string;
	    page?: WikiPage;

	    static createFrom(source: any = {}) {
	        return new WikiWriteResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.page = this.convertValues(source["page"], WikiPage);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace store {

	export class IssueFilter {
	    projectId: number;
	    keyword: string;
	    keywordMode: string;
	    createdFrom: string;
	    createdTo: string;
	    updatedFrom: string;
	    updatedTo: string;
	    statusName: string;
	    assigneeName: string;
	    customFieldFilters: customfield.Filter[];
	    limit: number;
	    offset: number;

	    static createFrom(source: any = {}) {
	        return new IssueFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.projectId = source["projectId"];
	        this.keyword = source["keyword"];
	        this.keywordMode = source["keywordMode"];
	        this.createdFrom = source["createdFrom"];
	        this.createdTo = source["createdTo"];
	        this.updatedFrom = source["updatedFrom"];
	        this.updatedTo = source["updatedTo"];
	        this.statusName = source["statusName"];
	        this.assigneeName = source["assigneeName"];
	        this.customFieldFilters = this.convertValues(source["customFieldFilters"], customfield.Filter);
	        this.limit = source["limit"];
	        this.offset = source["offset"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class JobSummary {
	    jobId: number;
	    kind: string;
	    projectId: number;
	    sourceFile: string;
	    sourceHash: string;
	    createdAt: string;
	    status: string;
	    total: number;
	    done: number;
	    failed: number;
	    pending: number;
	    sending: number;
	    conflict: number;
	    skipped: number;

	    static createFrom(source: any = {}) {
	        return new JobSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobId = source["jobId"];
	        this.kind = source["kind"];
	        this.projectId = source["projectId"];
	        this.sourceFile = source["sourceFile"];
	        this.sourceHash = source["sourceHash"];
	        this.createdAt = source["createdAt"];
	        this.status = source["status"];
	        this.total = source["total"];
	        this.done = source["done"];
	        this.failed = source["failed"];
	        this.pending = source["pending"];
	        this.sending = source["sending"];
	        this.conflict = source["conflict"];
	        this.skipped = source["skipped"];
	    }
	}
	export class UserFilter {
	    keyword: string;
	    roleType: number;
	    limit: number;

	    static createFrom(source: any = {}) {
	        return new UserFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keyword = source["keyword"];
	        this.roleType = source["roleType"];
	        this.limit = source["limit"];
	    }
	}
	export class UserRow {
	    id: number;
	    userCode: string;
	    name: string;
	    mailAddress: string;
	    roleType: number;
	    roleName: string;
	    teamNames: string[];
	    projectKeys: string[];
	    adminProjectKeys: string[];

	    static createFrom(source: any = {}) {
	        return new UserRow(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.userCode = source["userCode"];
	        this.name = source["name"];
	        this.mailAddress = source["mailAddress"];
	        this.roleType = source["roleType"];
	        this.roleName = source["roleName"];
	        this.teamNames = source["teamNames"];
	        this.projectKeys = source["projectKeys"];
	        this.adminProjectKeys = source["adminProjectKeys"];
	    }
	}
	export class WikiAttachment {
	    wikiId: number;
	    id: number;
	    name: string;
	    size: number;
	    created: string;
	    rawJson: string;

	    static createFrom(source: any = {}) {
	        return new WikiAttachment(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.wikiId = source["wikiId"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.created = source["created"];
	        this.rawJson = source["rawJson"];
	    }
	}
	export class WikiFilter {
	    projectId: number;
	    keyword: string;
	    keywordMode: string;

	    static createFrom(source: any = {}) {
	        return new WikiFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.projectId = source["projectId"];
	        this.keyword = source["keyword"];
	        this.keywordMode = source["keywordMode"];
	    }
	}

}

export namespace sync {

	export class WikiResult {
	    mode: string;
	    fetched: number;
	    upserted: number;
	    deleted: number;
	    warnings: string[];
	    durationMs: number;
	    disabled: boolean;

	    static createFrom(source: any = {}) {
	        return new WikiResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.fetched = source["fetched"];
	        this.upserted = source["upserted"];
	        this.deleted = source["deleted"];
	        this.warnings = source["warnings"];
	        this.durationMs = source["durationMs"];
	        this.disabled = source["disabled"];
	    }
	}

}
