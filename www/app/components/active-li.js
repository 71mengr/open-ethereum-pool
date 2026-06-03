import Ember from 'ember';

const { getOwner } = Ember;

export default Ember.Component.extend({
  tagName: 'li',
  classNameBindings: ['isActive:active:inactive'],

  router: Ember.computed(function(){
    return getOwner(this).lookup('router:main');
  }),

  isActive: Ember.computed('router.url', 'currentWhen', function(){
    var currentWhen = this.get('currentWhen');
    return this.get('router').isActive(currentWhen);
  })
});
