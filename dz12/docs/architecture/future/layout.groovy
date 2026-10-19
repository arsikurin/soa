import com.structurizr.view.Dimensions
import com.structurizr.view.Vertex

def positions = [
    'Покупатель': [200, 648],
    'Продавец': [200, 1903],
    'Web SPA': [1214, 938],
    'API Gateway': [2150, 750],
    'Users Service': [4010, 270],
    'Catalog Service': [3086, 493],
    'Orders Service': [2150, 1370],
    'Payments Service': [3086, 1730],
    'Notifications Service': [3086, 1010],
    'Users DB': [4934, 250],
    'Catalog DB': [4010, 610],
    'Orders DB': [3086, 1350],
    'Payments DB': [4010, 1478],
    'Notifications DB': [4010, 990],
    'Платежный провайдер': [3086, 2520],
    'Email-провайдер': [4010, 2280]
]

def routes = [
    ['Покупатель', 'Web SPA']: [[1124, 828]],
    ['Продавец', 'Web SPA']: [[1124, 2083]],
    ['Web SPA', 'API Gateway']: [[1856, 870]],
    ['API Gateway', 'Users Service']: [[2804, 244], [3728, 244]],
    ['API Gateway', 'Catalog Service']: [[2804, 613]],
    ['API Gateway', 'Orders Service']: [],
    ['Catalog Service', 'Users Service']: [[3728, 433]],
    ['Orders Service', 'Catalog Service']: [[2804, 802]],
    ['Orders Service', 'Notifications Service']: [[2804, 1130]],
    ['Orders Service', 'Orders DB']: [],
    ['Orders Service', 'Payments Service']: [[2804, 1731]],
    ['Notifications Service', 'Users Service']: [[3728, 865]],
    ['Users Service', 'Users DB']: [],
    ['Catalog Service', 'Catalog DB']: [[3728, 675]],
    ['Payments Service', 'Orders Service']: [[2804, 1920]],
    ['Payments Service', 'Payments DB']: [[3728, 1618]],
    ['Notifications Service', 'Notifications DB']: [],
    ['Web SPA', 'Платежный провайдер']: [[1856, 2640]],
    ['Payments Service', 'Платежный провайдер']: [[3010, 2240]],
    ['Платежный провайдер', 'Payments Service']: [[3520, 2240]],
    ['Notifications Service', 'Email-провайдер']: [[3600, 1340], [4500, 1340], [4500, 2400]]
]

view.disableAutomaticLayout()
view.mergeFromRemote = false
view.dimensions = new Dimensions(5544, 2960)
view.elements.each { elementView ->
    def xy = positions[elementView.element.name]
    assert xy != null : "Missing position: ${elementView.element.name}"
    elementView.x = xy[0]
    elementView.y = xy[1]
}
view.relationships.each { relationshipView ->
    def relationship = relationshipView.relationship
    def points = routes[[relationship.source.name, relationship.destination.name]]
    assert points != null : "Missing route: ${relationship.source.name} -> ${relationship.destination.name}"
    relationshipView.vertices = points.collect { new Vertex(it[0], it[1]) }
}
