// Plain data classes for the core API's JSON. See services/api/README.md.

import 'dart:convert';

double? _double(Object? value) => value is num ? value.toDouble() : null;
String _string(Object? value) => value is String ? value : (value?.toString() ?? '');
String? _optional(Object? value) => value is String && value.isNotEmpty ? value : null;

/// A bounding box in degrees.
class Bounds {
  const Bounds(this.west, this.south, this.east, this.north);

  static Bounds? fromJson(Object? json) {
    if (json is! List || json.length != 4 || json.any((v) => v is! num)) return null;
    final v = json.cast<num>().map((n) => n.toDouble()).toList();
    return Bounds(v[0], v[1], v[2], v[3]);
  }

  final double west, south, east, north;

  @override
  bool operator ==(Object other) =>
      other is Bounds && other.west == west && other.south == south && other.east == east && other.north == north;

  @override
  int get hashCode => Object.hash(west, south, east, north);
}

class Workspace {
  const Workspace({required this.slug, required this.name});
  factory Workspace.fromJson(Map<String, dynamic> json) =>
      Workspace(slug: _string(json['slug']), name: _string(json['name']));
  final String slug;
  final String name;
}

class Unit {
  const Unit({required this.code, required this.name, required this.levelSlug, required this.path, this.bounds});
  factory Unit.fromJson(Map<String, dynamic> json) => Unit(
    code: _string(json['code']),
    name: _string(json['name']),
    levelSlug: _string(json['levelSlug']),
    path: _string(json['path']),
    bounds: Bounds.fromJson(json['bounds']),
  );
  final String code;
  final String name;
  final String levelSlug;
  final String path;

  /// The unit's extent, for fitting the map to the user's jurisdiction.
  final Bounds? bounds;
}

class RoleAssignment {
  const RoleAssignment({required this.workspace, required this.hierarchy, required this.role, required this.unit});
  factory RoleAssignment.fromJson(Map<String, dynamic> json) => RoleAssignment(
    workspace: Workspace.fromJson(json['workspace'] as Map<String, dynamic>),
    hierarchy: _string(json['hierarchy']),
    role: _string(json['role']),
    unit: Unit.fromJson(json['unit'] as Map<String, dynamic>),
  );
  final Workspace workspace;
  final String hierarchy;
  final String role;
  final Unit unit;

  /// A plain-English name for the role.
  String get roleLabel => switch (role) {
    'state_admin' => 'State administrator',
    'division_officer' => 'Division officer',
    'field_staff' => 'Field staff',
    _ => role.replaceAll('_', ' '),
  };
}

class Profile {
  const Profile({required this.subject, required this.username, required this.roleAssignments});
  factory Profile.fromJson(Map<String, dynamic> json) => Profile(
    subject: _string(json['subject']),
    username: _string(json['username']),
    roleAssignments: [
      for (final item in (json['roleAssignments'] as List? ?? const []))
        RoleAssignment.fromJson(item as Map<String, dynamic>),
    ],
  );
  final String subject;
  final String username;
  final List<RoleAssignment> roleAssignments;

  /// Each workspace once, in the order the API lists the grants.
  List<Workspace> get workspaces {
    final seen = <String>{};
    return [
      for (final grant in roleAssignments)
        if (seen.add(grant.workspace.slug)) grant.workspace,
    ];
  }

  List<RoleAssignment> grantsIn(String slug) => roleAssignments.where((grant) => grant.workspace.slug == slug).toList();
}

class HierarchyLevel {
  const HierarchyLevel({required this.hierarchy, required this.depth, required this.slug, required this.labels});
  factory HierarchyLevel.fromJson(Map<String, dynamic> json) => HierarchyLevel(
    hierarchy: _string(json['hierarchy']),
    depth: (json['depth'] as num?)?.toInt() ?? 0,
    slug: _string(json['slug']),
    labels: {for (final entry in ((json['labels'] as Map?) ?? const {}).entries) '${entry.key}': _string(entry.value)},
  );
  final String hierarchy;
  final int depth;
  final String slug;
  final Map<String, String> labels;

  String get label => labels['en'] ?? slug;
}

/// The workspace geography from `GET /api/v1/workspaces/{slug}`.
class WorkspaceMap {
  const WorkspaceMap({required this.version, required this.boundary, required this.levels});
  factory WorkspaceMap.fromJson(Map<String, dynamic> json) => WorkspaceMap(
    version: _string(json['version']),
    boundary: Bounds.fromJson(json['boundary']),
    levels: [
      for (final item in (json['levels'] as List? ?? const [])) HierarchyLevel.fromJson(item as Map<String, dynamic>),
    ],
  );
  final String version;
  final Bounds? boundary;
  final List<HierarchyLevel> levels;

  /// The workspace's own name for a level, such as "Beat".
  String levelLabel(String hierarchy, String slug) {
    for (final level in levels) {
      if (level.slug == slug && (hierarchy.isEmpty || level.hierarchy == hierarchy)) return level.label;
    }
    return slug.isEmpty ? '' : slug[0].toUpperCase() + slug.substring(1);
  }

  /// Levels of [hierarchy], deepest first, as an alert's place lists them.
  List<HierarchyLevel> placeOrder(String hierarchy) {
    final rows = levels.where((l) => l.hierarchy == hierarchy && l.depth > 0).toList()
      ..sort((a, b) => b.depth.compareTo(a.depth));
    return rows;
  }
}

/// One level of an alert's place, such as Beat 05.
class Place {
  const Place({required this.code, required this.name});
  final String code;
  final String name;
}

enum ForestClass {
  inside('inside', 'Inside forest'),
  nearEdge('near_edge', 'Near the forest edge'),
  outside('outside', 'Outside forest'),
  fixedHeatSource('fixed_heat_source', 'Fixed heat source');

  const ForestClass(this.slug, this.label);
  final String slug;
  final String label;

  static ForestClass? fromSlug(String? slug) {
    for (final value in values) {
      if (value.slug == slug) return value;
    }
    return null;
  }
}

/// The queue an alert was routed to.
enum AlertQueue {
  unit('unit', 'My area'),
  edgeReview('edge_review', 'Edge review'),
  triage('triage', 'Triage');

  const AlertQueue(this.slug, this.label);
  final String slug;
  final String label;

  static AlertQueue? fromSlug(String? slug) {
    for (final value in values) {
      if (value.slug == slug) return value;
    }
    return null;
  }
}

/// An alert row from the queue or its detail route.
class Alert {
  const Alert({
    required this.id,
    required this.detectionId,
    required this.queue,
    required this.status,
    required this.stepLabel,
    required this.stepTime,
    required this.overdue,
    required this.observedAt,
    this.raisedAt,
    required this.latitude,
    required this.longitude,
    required this.forestClass,
    required this.unitPath,
    required this.placeHierarchy,
    required this.places,
    this.triage,
    this.source,
    this.product,
    this.confidence,
    this.frpMw,
  });

  factory Alert.fromJson(Map<String, dynamic> json) {
    final places = <String, Place>{};
    final raw = json['places'];
    if (raw is Map) {
      for (final entry in raw.entries) {
        final value = entry.value;
        if (value is Map) {
          places['${entry.key}'] = Place(code: _string(value['code']), name: _string(value['name']));
        }
      }
    }
    return Alert(
      id: _string(json['id']),
      detectionId: _string(json['detection_id']),
      queue: _string(json['queue']),
      status: _string(json['status']),
      stepLabel: _string(json['step_label']),
      stepTime: DateTime.tryParse(_string(json['step_time'])),
      overdue: json['overdue'] == true,
      observedAt:
          DateTime.tryParse(_string(json['observed_time'])) ??
          DateTime.fromMillisecondsSinceEpoch(((json['observed_at'] as num?) ?? 0).toInt() * 1000, isUtc: true),
      raisedAt: DateTime.tryParse(_string(json['created_at'])),
      latitude: _double(json['latitude']) ?? 0,
      longitude: _double(json['longitude']) ?? 0,
      forestClass: _string(json['forest_class']),
      unitPath: _optional(json['unit_path']),
      placeHierarchy: _string(json['place_hierarchy']),
      places: places,
      triage: _optional(json['triage']),
      source: _optional(json['source']),
      product: _optional(json['product']),
      confidence: _optional(json['confidence']),
      frpMw: _double(json['frp_mw']),
    );
  }

  final String id;
  final String detectionId;
  final String queue;
  final String status;
  final String stepLabel;
  final DateTime? stepTime;
  final bool overdue;
  final DateTime observedAt;
  final DateTime? raisedAt;
  final double latitude;
  final double longitude;
  final String forestClass;
  final String? unitPath;
  final String placeHierarchy;
  final Map<String, Place> places;
  final String? triage;
  final String? source;
  final String? product;
  final String? confidence;
  final double? frpMw;

  ForestClass? get forest => ForestClass.fromSlug(forestClass);
  AlertQueue? get alertQueue => AlertQueue.fromSlug(queue);

  /// The most specific named place, such as "Beat 05".
  String placeName(WorkspaceMap? map) {
    final order = map?.placeOrder(placeHierarchy).map((l) => l.slug) ?? const ['beat', 'range', 'division', 'circle'];
    for (final slug in order) {
      final place = places[slug];
      if (place != null && place.name.isNotEmpty) return place.name;
    }
    return 'No unit';
  }
}

class AlertPage {
  const AlertPage({required this.items, required this.counts, required this.canReview, this.nextCursor});
  factory AlertPage.fromJson(Map<String, dynamic> json) => AlertPage(
    items: [for (final item in (json['items'] as List? ?? const [])) Alert.fromJson(item as Map<String, dynamic>)],
    counts: {
      for (final entry in ((json['counts'] as Map?) ?? const {}).entries) '${entry.key}': (entry.value as num).toInt(),
    },
    canReview: json['canReview'] == true,
    nextCursor: _optional(json['nextCursor']),
  );
  final List<Alert> items;
  final Map<String, int> counts;
  final bool canReview;
  final String? nextCursor;
}

/// A workflow step the caller may take next.
class WorkflowMove {
  const WorkflowMove({
    required this.step,
    required this.label,
    required this.locationRequired,
    required this.canOverride,
    required this.terminal,
    this.extended = false,
  });
  factory WorkflowMove.fromJson(Map<String, dynamic> json) => WorkflowMove(
    step: _string(json['step']),
    label: _string(json['label']),
    locationRequired: json['location_required'] == true,
    canOverride: json['can_override'] == true,
    terminal: json['terminal'] == true,
    extended: json['extended'] == true,
  );
  final String step;
  final String label;
  final bool locationRequired;
  final bool canOverride;
  final bool terminal;
  final bool extended;
}

class HistoryEntry {
  const HistoryEntry({
    required this.step,
    required this.label,
    required this.at,
    this.actor,
    this.note = '',
    this.reason = '',
    this.latitude,
    this.longitude,
    this.reportPending = false,
    this.actualAt,
    this.estimatedAt,
    this.syncedAt,
    this.response,
  });
  factory HistoryEntry.fromJson(Map<String, dynamic> json) => HistoryEntry(
    step: _string(json['step']),
    label: _string(json['label']),
    at: DateTime.tryParse(_string(json['at'])) ?? DateTime.fromMillisecondsSinceEpoch(0, isUtc: true),
    actor: _optional(json['actor']),
    note: _string(json['note']),
    reason: _string(json['reason']),
    latitude: _double(json['latitude']),
    longitude: _double(json['longitude']),
    reportPending: json['reportPending'] == true,
    actualAt: DateTime.tryParse(_string(json['actualAt'])),
    estimatedAt: DateTime.tryParse(_string(json['estimatedAt'])),
    syncedAt: DateTime.tryParse(_string(json['syncedAt'])),
    response: json['response'] as Map<String, dynamic>?,
  );
  final String step;
  final String label;
  final DateTime at;
  final String? actor;
  final String note;
  final String reason;
  final double? latitude;
  final double? longitude;
  final bool reportPending;
  final DateTime? actualAt;
  final DateTime? estimatedAt;
  final DateTime? syncedAt;
  final Map<String, dynamic>? response;
}

class AlertHistory {
  const AlertHistory({required this.entries, required this.moves, this.offlineMoves = const {}});
  factory AlertHistory.fromJson(Map<String, dynamic> json) => AlertHistory(
    entries: [
      for (final item in (json['entries'] as List? ?? const [])) HistoryEntry.fromJson(item as Map<String, dynamic>),
    ],
    moves: _moves(json['moves']),
    offlineMoves: {
      for (final entry in (json['offlineMoves'] as Map<String, dynamic>? ?? const {}).entries)
        entry.key: _moves(entry.value),
    },
  );
  final List<HistoryEntry> entries;
  final List<WorkflowMove> moves;
  final Map<String, List<WorkflowMove>> offlineMoves;

  static List<WorkflowMove> _moves(Object? data) => [
    for (final item in data as List? ?? const []) WorkflowMove.fromJson(item as Map<String, dynamic>),
  ];
}

/// A workflow step to record. [clientId] makes a retry safe: the server
/// answers a repeat of the same request with the first receipt.
class StepRequest {
  const StepRequest({
    required this.step,
    required this.clientId,
    this.note = '',
    this.reason = '',
    this.latitude,
    this.longitude,
    this.clientTime,
    this.actualAt,
    this.estimatedAt,
    this.response,
  });
  factory StepRequest.fromJson(Map<String, dynamic> json) {
    final location = json['location'] as Map<String, dynamic>?;
    return StepRequest(
      step: _string(json['step']),
      clientId: _string(json['clientId']),
      clientTime: DateTime.tryParse(_string(json['clientTime'])),
      actualAt: DateTime.tryParse(_string(json['actualAt'])),
      estimatedAt: DateTime.tryParse(_string(json['estimatedAt'])),
      response: json['response'] as Map<String, dynamic>?,
      note: _string(json['note']),
      reason: _string(json['reason']),
      latitude: _double(location?['latitude']),
      longitude: _double(location?['longitude']),
    );
  }

  final String step;
  final String clientId;
  final String note;
  final String reason;
  final double? latitude;
  final double? longitude;
  final DateTime? clientTime;
  final DateTime? actualAt;
  final DateTime? estimatedAt;
  final Map<String, dynamic>? response;

  Map<String, dynamic> toJson() => {
    'step': step,
    'clientId': clientId,
    if (note.isNotEmpty) 'note': note,
    if (reason.isNotEmpty) 'reason': reason,
    if (latitude != null && longitude != null) 'location': {'latitude': latitude, 'longitude': longitude},
    if (clientTime != null) 'clientTime': clientTime!.toUtc().toIso8601String(),
    if (actualAt != null) 'actualAt': actualAt!.toUtc().toIso8601String(),
    if (estimatedAt != null) 'estimatedAt': estimatedAt!.toUtc().toIso8601String(),
    if (response != null) 'response': response,
  };
}

class StepReceipt {
  const StepReceipt({required this.id, required this.step, required this.at, this.syncedAt});
  factory StepReceipt.fromJson(Map<String, dynamic> json) => StepReceipt(
    id: _string(json['id']),
    step: _string(json['step']),
    at: DateTime.tryParse(_string(json['at'])) ?? DateTime.now().toUtc(),
    syncedAt: DateTime.tryParse(_string(json['syncedAt'])),
  );
  final String id;
  final String step;
  final DateTime at;
  final DateTime? syncedAt;
}

/// Where push messages reach this phone: a UnifiedPush endpoint and its
/// Web Push keys, or a Firebase token when that optional service is used.
class PushAddress {
  const PushAddress({required this.provider, required this.endpoint, this.publicKey, this.auth});
  final String provider;
  final String endpoint;
  final String? publicKey;
  final String? auth;

  Map<String, dynamic> toJson() => {'provider': provider, 'endpoint': endpoint, 'publicKey': ?publicKey, 'auth': ?auth};

  @override
  bool operator ==(Object other) =>
      other is PushAddress &&
      other.provider == provider &&
      other.endpoint == endpoint &&
      other.publicKey == publicKey &&
      other.auth == auth;

  @override
  int get hashCode => Object.hash(provider, endpoint, publicKey, auth);
}

/// What the phone tells the API when it registers.
class DeviceRegistration {
  const DeviceRegistration({required this.platform, required this.appVersion, this.push});
  final String platform;
  final String appVersion;
  final PushAddress? push;

  Map<String, dynamic> toJson() => {'platform': platform, 'appVersion': appVersion, 'push': push?.toJson()};
}

/// The state of one registered phone, as the API reports it.
enum DeviceStatus {
  active('Signed in', 'active'),
  signedOut('Signed out', 'signed_out'),
  removed('Removed', 'removed'),
  wipePending('Waiting to delete its data', 'wipe_pending'),
  wiped('Data deleted', 'wiped');

  const DeviceStatus(this.label, this.slug);
  final String label;
  final String slug;

  static DeviceStatus parse(Object? value) =>
      DeviceStatus.values.where((s) => s.slug == value).firstOrNull ?? DeviceStatus.removed;
}

class Device {
  const Device({
    required this.id,
    required this.platform,
    required this.appVersion,
    required this.push,
    required this.status,
    required this.registeredAt,
    required this.lastSeenAt,
    this.current = false,
  });

  factory Device.fromJson(Map<String, dynamic> json) => Device(
    id: _string(json['id']),
    platform: _string(json['platform']),
    appVersion: _string(json['appVersion']),
    push: json['push'] == true,
    status: DeviceStatus.parse(json['status']),
    registeredAt: DateTime.tryParse(_string(json['registeredAt'])) ?? DateTime.fromMillisecondsSinceEpoch(0),
    lastSeenAt: DateTime.tryParse(_string(json['lastSeenAt'])) ?? DateTime.fromMillisecondsSinceEpoch(0),
    current: json['current'] == true,
  );

  final String id;
  final String platform;
  final String appVersion;

  /// True when the phone can receive alert notifications now.
  final bool push;
  final DeviceStatus status;
  final DateTime registeredAt;
  final DateTime lastSeenAt;

  /// True for the phone making the request.
  final bool current;

  String get platformLabel => switch (platform) {
    'android' => 'Android phone',
    'ios' => 'iPhone',
    'web' => 'Browser test build',
    _ => 'Phone',
  };
}

/// The answer to a registration.
class DeviceReceipt {
  const DeviceReceipt({required this.device, required this.pushProviders, this.pushProblem});
  factory DeviceReceipt.fromJson(Map<String, dynamic> json) => DeviceReceipt(
    device: Device.fromJson(json['device'] as Map<String, dynamic>? ?? const {}),
    pushProviders: (json['pushProviders'] as List? ?? const []).whereType<String>().toList(),
    pushProblem: _optional(json['pushProblem']),
  );
  final Device device;

  /// The push services the server sends through.
  final List<String> pushProviders;

  /// Why the server will not send to this phone's push address, in plain English.
  final String? pushProblem;
}

/// A push message, decoded. It holds IDs and a generic text only; the app
/// fetches the alert itself over the signed-in API.
class PushNotice {
  const PushNotice({
    required this.type,
    this.notification,
    this.workspace,
    this.alert,
    this.title = 'New fire alert',
    this.body = 'Open the field app to see it.',
  });

  /// Reads a message, or returns null for anything the app does not know.
  static PushNotice? parse(List<int> bytes) {
    try {
      final json = jsonDecode(utf8.decode(bytes));
      if (json is! Map<String, dynamic> || json['v'] != 1) return null;
      final type = json['type'];
      if (type == 'device_check') return const PushNotice(type: 'device_check');
      if (type != 'notification') return null;
      final notification = _optional(json['notification']);
      final workspace = _optional(json['workspace']);
      final alert = _optional(json['alert']);
      if (notification == null || int.tryParse(notification) == null || workspace == null) return null;
      return PushNotice(
        type: 'notification',
        notification: notification,
        workspace: workspace,
        alert: alert != null && int.tryParse(alert) != null ? alert : null,
        title: _shortText(json['title'], 'New fire alert'),
        body: _shortText(json['body'], 'Open the field app to see it.'),
      );
    } on FormatException {
      return null;
    }
  }

  /// The data kept with a shown notification, for opening its alert.
  static PushNotice? fromPayload(String? payload) => payload == null ? null : parse(utf8.encode(payload));

  final String type;
  final String? notification;
  final String? workspace;
  final String? alert;
  final String title;
  final String body;

  /// The Android notification ID: one per server notification, so a repeated
  /// message replaces the first instead of adding a second.
  int get id => (int.tryParse(notification ?? '') ?? 0) & 0x7fffffff;

  String toPayload() => jsonEncode({
    'v': 1,
    'type': type,
    'notification': notification,
    'workspace': workspace,
    'alert': alert,
    'title': title,
    'body': body,
  });
}

String _shortText(Object? value, String fallback) =>
    value is String && value.trim().isNotEmpty && value.length <= 120 ? value.trim() : fallback;
