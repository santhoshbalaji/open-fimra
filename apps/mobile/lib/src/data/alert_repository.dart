import '../api/api_client.dart';
import '../api/models.dart';

/// The app's single way to read and change field data.
///
/// The phone implementation can read a saved copy of the workspace.
abstract interface class AlertRepository {
  Future<Profile> profile();
  Future<WorkspaceMap> workspace(String slug);

  /// A page of the alerts the user may see. [queue] is null for all queues.
  Future<AlertPage> alerts(String slug, {AlertQueue? queue, String? cursor});

  /// The alert for a map point, or null when the user has no alert for it.
  Future<Alert?> alertForDetection(String slug, String detectionId);

  Future<Alert> alert(String slug, String id);
  Future<AlertHistory> history(String slug, String id);
  Future<StepReceipt> recordStep(String slug, String alertId, StepRequest request);

  /// Tile address templates for the map.
  String referenceTiles(String slug);
  String detectionTiles(String slug);
}

/// Reads and writes straight through the core API.
class OnlineAlertRepository implements AlertRepository {
  OnlineAlertRepository(this.api);

  final FimraApi api;

  /// How far back the list and map look, in hours. The API's limit is 720.
  static const windowHours = 168;

  @override
  Future<Profile> profile() => api.me();

  @override
  Future<WorkspaceMap> workspace(String slug) => api.workspace(slug);

  @override
  Future<AlertPage> alerts(String slug, {AlertQueue? queue, String? cursor}) =>
      api.alerts(slug, queue: queue?.slug, cursor: cursor, hours: windowHours, limit: 50);

  @override
  Future<Alert?> alertForDetection(String slug, String detectionId) async {
    final page = await api.alerts(slug, detection: detectionId, hours: 720, limit: 1);
    return page.items.isEmpty ? null : page.items.first;
  }

  @override
  Future<Alert> alert(String slug, String id) => api.alert(slug, id);

  @override
  Future<AlertHistory> history(String slug, String id) => api.history(slug, id);

  @override
  Future<StepReceipt> recordStep(String slug, String alertId, StepRequest request) =>
      api.recordStep(slug, alertId, request);

  @override
  String referenceTiles(String slug) => api.referenceTiles(slug);

  @override
  String detectionTiles(String slug) => api.detectionTiles(slug, hours: windowHours);
}
