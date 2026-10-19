import com.structurizr.view.Dimensions

def positions = [
    'API Gateway': [220, 180],
    'Users Service': [220, 520],
    'Catalog Service': [220, 860],
    'Orders Service': [220, 1200],
    'Payments Service': [220, 1540],
    'Notifications Service': [220, 1880],
    'OTel Collector': [1480, 1030],
    'Jaeger': [2380, 1030]
]

view.disableAutomaticLayout()
view.mergeFromRemote = false
view.dimensions = new Dimensions(2940, 2320)
view.elements.each { elementView ->
    def xy = positions[elementView.element.name]
    assert xy != null : "Missing position: ${elementView.element.name}"
    elementView.x = xy[0]
    elementView.y = xy[1]
}
view.relationships.each { relationshipView ->
    assert relationshipView.relationship.hasTag('TraceExport')
    relationshipView.vertices = []
}
