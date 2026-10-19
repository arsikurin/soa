import com.structurizr.view.Dimensions
import com.structurizr.view.Vertex

def positions = [
    'API Gateway': [50, 600],
    'Заказы и промокоды': [1020, 280],
    'Координатор 2PC': [1970, 280],
    'Восстановление': [1970, 1100],
    'Catalog': [3000, 50],
    'Orders DB': [3000, 1100]
]
view.disableAutomaticLayout()
view.mergeFromRemote = false
view.dimensions = new Dimensions(3600, 1900)
view.elements.each { ev ->
    def xy = positions[ev.element.name]
    ev.x = xy[0]
    ev.y = xy[1]
}
view.relationships.each { rv ->
    def source = rv.relationship.source.name
    def target = rv.relationship.destination.name
    rv.vertices = []
    if (source == 'API Gateway' && target == 'Catalog') {
        rv.vertices = [new Vertex(680, 170), new Vertex(2750, 170)]
    } else if (source == 'Заказы и промокоды' && target == 'Orders DB') {
        rv.vertices = [new Vertex(1230, 1500), new Vertex(2900, 1500)]
    } else if (source == 'Координатор 2PC' && target == 'Orders DB') {
        rv.vertices = [new Vertex(2720, 800)]
    }
}
